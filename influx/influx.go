/*
Copyright © 2020 Anton Kramarev
Copyright © 2024 Perfana Software B.V.

Permission is hereby granted, free of charge, to any person obtaining a copy
of this software and associated documentation files (the "Software"), to deal
in the Software without restriction, including without limitation the rights
to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
copies of the Software, and to permit persons to whom the Software is
furnished to do so, subject to the following conditions:

The above copyright notice and this permission notice shall be included in
all copies or substantial portions of the Software.

THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN
THE SOFTWARE.
*/

package influx

import (
	"context"
	"fmt"
	"runtime"
	"sync"
	"time"

	l "github.com/perfana/x2i/logger"
	influxdb2 "github.com/influxdata/influxdb-client-go/v2"
	"github.com/influxdata/influxdb-client-go/v2/api"
	"github.com/influxdata/influxdb-client-go/v2/api/write"
	"github.com/spf13/cobra"
)

type testInfo struct {
	systemUnderTest string
	testEnvironment string
	simulationName  string
	description     string
	nodeName        string
	testStartTime   time.Time
}

type userLineData struct {
	timestamp time.Time
	scenario  string
	status    string
}

var (
	client     influxdb2.Client
	writeAPI   api.WriteAPIBlocking
	org        string
	bucket     string
	info       testInfo
	lastPoint  time.Time
	maxPoints  uint

	// pc is a channel to send all point from parser to
	pc = make(chan *write.Point, 1000)
	// uc is a channel for userLineData processing
	uc = make(chan userLineData, 1000)

	// TODO: parameterize later
	writeDataTimeout = 5
)

// InitTestInfo collect basic test information to be used by Influx client
func InitTestInfo(systemUnderTest, testEnvironment, simulationName, description, nodeName string, testStartTime time.Time) {
	info = testInfo{
		systemUnderTest: systemUnderTest,
		testEnvironment: testEnvironment,
		simulationName:  simulationName,
		description:     description,
		nodeName:        nodeName,
		testStartTime:   testStartTime,
	}
}

// NewPoint is mostly an alias for standard NewPoint function from influx package,
// except timestamp is required
func NewPoint(name string, tags map[string]string, fields map[string]interface{}, t time.Time) (*write.Point, error) {
	return influxdb2.NewPoint(name, tags, fields, t), nil
}

// SendPoint sends point to the channel listened by metrics consumer
func SendPoint(p *write.Point) {
	pc <- p
}

func sendBatch(points []*write.Point) {
	const retries = 5

	// Retry mechanism for batch points sending
	var errCounter int
SendLoop:
	for {
		err := writeAPI.WritePoint(context.Background(), points...)
		if err != nil {
			l.Errorf("Error sending points batch to InfluxDB: %v\n", err)
			errCounter++
			if errCounter == retries {
				l.Errorf("Failed to send %d points as batch to server\n", len(points))
				return
			}
			time.Sleep(2 * time.Second)
		} else {
			break SendLoop
		}
	}

	if errCounter > 0 {
		l.Infof("%d points successfully sent after %d retries\n", len(points), errCounter)
		return
	}

	l.Debugf("Successfully written %d points to DB\n", len(points))
}

// SendUserLineData takes a line with user data and adds it to the processing list
func SendUserLineData(timestamp time.Time, scenario, status string) {
	uld := userLineData{timestamp, scenario, status}

	uc <- uld
}

func sendUserData(m map[string]int, ts time.Time) ([]*write.Point, error) {
	// Prepare points
	points := make([]*write.Point, 0, len(m))
	for k, v := range m {
		point := influxdb2.NewPoint(
			"users",
			map[string]string{
				"scenario":        k,
				"testEnvironment": info.testEnvironment,
				"systemUnderTest": info.systemUnderTest,
				"nodeName":        info.nodeName,
			},
			map[string]interface{}{
				"active": v,
			},
			ts,
		)

		points = append(points, point)
	}

	return points, nil
}

func usersProcessor(ctx context.Context, wg *sync.WaitGroup) {
	// Send current user state to database each N seconds
	const timeRangeLen = 5
	defer wg.Done()

	// Workaround:
	// Wait for testInfo to fill
	for {
		if !info.testStartTime.IsZero() {
			break
		}
		time.Sleep(time.Second)
	}

	secondFrom := info.testStartTime.Round(time.Second)
	secondTo := secondFrom.Add(time.Second * timeRangeLen)
	usersMap := make(map[string]int)

CollectorLoop:
	for {
		select {
		// If an external cancellation signal is received
		case <-ctx.Done():
			// Init closeup
			closingPointTime := lastPoint
			var points []*write.Point
			// Fill empty points with last available data
			// Last point in buffer should always be sent. So this is an imitation of do-while loop
			for {
				// Advance searching range for next N seconds
				secondFrom, secondTo = secondTo, secondTo.Add(time.Second*timeRangeLen)

				// Collect remaining points
				pts, err := sendUserData(usersMap, secondFrom)
				if err != nil {
					l.Errorf("Failed to send user data: %v", err)
					continue
				}
				points = append(points, pts...)

				// Stop the loop when meeting the closing point time
				if !secondTo.Before(closingPointTime) {
					break
				}
			}
			// If total amount of points is higher than allowed batch amount
			// it is split and sent in batches
			for len(points) > int(maxPoints) {
				sendBatch(points[:int(maxPoints)])
				points = points[int(maxPoints):]
			}
			sendBatch(points)

			break CollectorLoop

		// On each new user line data
		case p := <-uc:
		SearcherLoop:
			for {
				// If point is somehow from the past
				if p.timestamp.Before(secondFrom) {
					// Then we just update the map
					switch p.status {
					case "START":
						usersMap[p.scenario]++
					case "END":
						usersMap[p.scenario]--
					}

					break SearcherLoop
				}

				// TODO: May combine with previous one later
				// If timestamp is a part of the current time range
				if (p.timestamp.After(secondFrom) || p.timestamp.Equal(secondFrom)) && p.timestamp.Before(secondTo) {
					// We update the map
					switch p.status {
					case "START":
						usersMap[p.scenario]++
					case "END":
						usersMap[p.scenario]--
					}

					break SearcherLoop
				}

				// Else we assume this time range is done and advance searching range for next N seconds
				secondFrom, secondTo = secondTo, secondTo.Add(time.Second*timeRangeLen)

				// And send data for previous range
				points, err := sendUserData(usersMap, secondFrom)
				if err != nil {
					l.Errorf("Failed to send user data: %v", err)
					continue
				}
				for _, p := range points {
					pc <- p
				}

				// Loop is then advanced looking for suitable range
			}
		}
	}
}

func metricsPointsCollector(ctx context.Context, wg *sync.WaitGroup) {
	defer wg.Done()
	points := make([]*write.Point, 0, int(maxPoints))

	timer := time.NewTimer(time.Second * time.Duration(writeDataTimeout))
CollectorLoop:
	for {
		select {
		// Send points after timer expires
		case <-timer.C:
			if len(points) > 0 {
				sendBatch(points)
				// After sending points to server clear points buffer
				points = make([]*write.Point, 0, int(maxPoints))
			}
			// Reset timer
			timer.Reset(time.Second * time.Duration(writeDataTimeout))
		// When point is received on the channel
		case p := <-pc:
			points = append(points, p)
			// Send batch points when batch capacity is reached
			if len(points) == int(maxPoints) {
				sendBatch(points)
				// After sending points to server clear points buffer
				points = make([]*write.Point, 0, maxPoints)
				// Reset timer
				timer.Reset(time.Second * time.Duration(writeDataTimeout))
			}
			// Each point received saves its timestamp for use as a closing point
			// Don't use users data because it has aggregated time stamp instead of concrete one
			if p.Name() != "users" {
				lastPoint = p.Time()
			}		// Await for external stop signal
		case <-ctx.Done():
			// Send any unsent points
			if len(points) > 0 {
				sendBatch(points)
				points = make([]*write.Point, 0, int(maxPoints))
			}
			break CollectorLoop
		}
	}
}

func sendClosingPoint() {
	// If info struct is empty, then parsing of file did not start,
	// so there is no need to send closing point
	if info.testStartTime.IsZero() {
		l.Infoln("Skipping stop test point write...")
		return
	}

	// Create a point signifying a test end
	p := influxdb2.NewPoint(
		"tests",
		map[string]string{
			"action":          "end",
			"simulation":      info.simulationName,
			"testEnvironment": info.testEnvironment,
			"systemUnderTest": info.systemUnderTest,
			"nodeName":        info.nodeName,
		},
		map[string]interface{}{
			"description": info.description,
		},
		// Add 5 seconds to the time since last point was received
		lastPoint.Add(time.Second*5),
	)

	sendBatch([]*write.Point{p})
}

// StartProcessing starts consumers that receive points from parser and send to
// InfluxDB server
func StartProcessing(ctx context.Context, owg *sync.WaitGroup) {
	defer owg.Done()

	l.Infoln("Starting consumers for parser results")
	wg := &sync.WaitGroup{}

	// start requests consumer
	upCtx, upCancel := context.WithCancel(context.Background())
	mpcCtx, mpcCancel := context.WithCancel(context.Background())
	wg.Add(2)
	go usersProcessor(upCtx, wg)
	go metricsPointsCollector(mpcCtx, wg)

	// Wait for external stop signal
	<-ctx.Done()

	l.Infoln("Stopping all points processor...")
	upCancel()
	mpcCancel() // This should be the last one

	wg.Wait()
	sendClosingPoint()
	l.Infoln("Points processor finished")

	err := CloseDBConnection()
	if err != nil {
		l.Errorf("Failed to close DB connection: %v", err)
	}
}

// InitInfluxConnection establishes connection to InfluxDB database
// and checks if it is successful
func InitInfluxConnection(cmd *cobra.Command) error {	// Get parameters from command flags
	token, _ := cmd.Flags().GetString("token")
	address, _ := cmd.Flags().GetString("address")
	org, _ = cmd.Flags().GetString("org")
	bucket, _ = cmd.Flags().GetString("bucket")
	maxPoints, _ = cmd.Flags().GetUint("max-batch-size")
	detached, _ := cmd.Flags().GetBool("detached")
	
	// Get InfluxDB v1 parameters for backward compatibility
	username, _ := cmd.Flags().GetString("username")
	password, _ := cmd.Flags().GetString("password")
	database, _ := cmd.Flags().GetString("database")
	
	// Check if we should use InfluxDB v1 compatibility (when username/password/database are provided)
	useV1Compatibility := (username != "" || password != "" || database != "")
	
	// If database is provided but bucket is not, use database as bucket
	if bucket == "" && database != "" {
		bucket = database
	}
	
	// Create a new client with InfluxDB v2 API
	appName := fmt.Sprintf("x2i-http-client-%s(%s)", cmd.Version, runtime.Version())
		if useV1Compatibility {
		// Check if we're missing required parameters for v1 mode
		if database == "" {
			return fmt.Errorf("when using InfluxDB v1, a database must be provided with the --database flag")
		}
		
		// Create v2 client with v1 compatibility options
		v1Token := fmt.Sprintf("%s:%s", username, password)
		client = influxdb2.NewClientWithOptions(
			address,
			v1Token,
			influxdb2.DefaultOptions().
				SetApplicationName(appName).
				SetHTTPRequestTimeout(60),
		)
		
		// When using v1 compatibility mode, org is "-" and bucket is the database name
		org = "-" // This is the standard convention for v1 compatibility in InfluxDB v2
		bucket = database
		
		l.Infof("Using InfluxDB v1 compatibility mode (database: %s)", database)
	} else {
		// Create standard v2 client
		client = influxdb2.NewClientWithOptions(
			address,
			token,
			influxdb2.DefaultOptions().
				SetApplicationName(appName).
				SetHTTPRequestTimeout(60),
		)
	}
	// Check if we're missing required parameters for v2 mode
	if !useV1Compatibility && (token == "" || org == "" || bucket == "") {
		if token == "" {
			return fmt.Errorf("when using InfluxDB v2, a token must be provided with the --token flag")
		}
		if org == "" {
			return fmt.Errorf("when using InfluxDB v2, an organization must be provided with the --org flag")
		}
		if bucket == "" {
			return fmt.Errorf("when using InfluxDB v2, a bucket must be provided with the --bucket flag")
		}
	}
	
	// Create write API blocking
	writeAPI = client.WriteAPIBlocking(org, bucket)

	// Sleep for 15 seconds to allow influxdb to start
	// This is needed for setups where influxdb is starting in parallel
	time.Sleep(15 * time.Second)

	// Check health
	health, err := client.Health(context.Background())
	if err != nil {
		return fmt.Errorf("connection with InfluxDB at %s could not be established. Error: %w", address, err)
	}
	
	if health.Status != "pass" {
		return fmt.Errorf("connection with InfluxDB at %s is not healthy. Status: %s", address, health.Status)
	}
	
	// Check if bucket exists with a simple query
	queryAPI := client.QueryAPI(org)
	_, err = queryAPI.Query(context.Background(), fmt.Sprintf(`from(bucket:"%s") |> range(start: -1m) |> limit(n:1)`, bucket))
	if err != nil {
		return fmt.Errorf("bucket '%s' test query failed with error: %w", bucket, err)
	}
	
	l.Infof("Successfully connected to InfluxDB at %s (bucket: %s, org: %s)", address, bucket, org)

	if !detached {
		l.Infof("Connection with InfluxDB at %s successfully established\n", address)
		return nil
	}

	return CloseDBConnection()
}

// CloseDBConnection just closes a connection to database when called
func CloseDBConnection() error {
	client.Close()
	return nil
}
