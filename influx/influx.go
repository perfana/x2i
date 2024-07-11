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
	_ "github.com/influxdata/influxdb1-client" // workaround from client documentation
	client "github.com/influxdata/influxdb1-client/v2"
	infc "github.com/influxdata/influxdb1-client/v2"
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
	c         infc.Client
	dbName    string
	info      testInfo
	lastPoint time.Time
	maxPoints uint

	// pc is a channel to send all point from parser to
	pc = make(chan *infc.Point, 1000)
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

// NewPoint is mostly an alias fo standard NewPoint function from influx package,
// except timestamp is required
func NewPoint(name string, tags map[string]string, fields map[string]interface{}, t time.Time) (*infc.Point, error) {
	return infc.NewPoint(name, tags, fields, t)
}

// SendPoint sends point to the channel listened by metrics consumer
func SendPoint(p *infc.Point) {
	pc <- p
}

func sendBatch(points []*infc.Point) {
	const retries = 5

	bp, _ := infc.NewBatchPoints(infc.BatchPointsConfig{
		Precision: "ns",
		Database:  dbName,
	})
	bp.AddPoints(points)

	// Retry mechanism for batch points sending
	var errCounter int
SendLoop:
	for {
		err := c.Write(bp)
		if err != nil {
			l.Errorf("Error sending points batch to InfluxDB: %v\n", err)
			errCounter++
			if errCounter == retries {
				l.Errorf("Failed to send %d points as batch to server\n", len(points))
				return
			}
			time.Sleep(2 * time.Second)
		}
		break SendLoop
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

func sendUserData(m map[string]int, ts time.Time) ([]*client.Point, error) {
	// Prepare points
	points := make([]*client.Point, 0, len(m))
	for k, v := range m {
		point, err := client.NewPoint(
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
		if err != nil {
			return nil, fmt.Errorf("Error creating new point with user data: %w", err)
		}

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
			var points []*client.Point
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
	points := make([]*infc.Point, 0, int(maxPoints))

	timer := time.NewTimer(time.Second * time.Duration(writeDataTimeout))
CollectorLoop:
	for {
		select {
		// Send points after timer expires
		case <-timer.C:
			if len(points) > 0 {
				sendBatch(points)
				// After sending points to server clear points buffer
				points = make([]*infc.Point, 0, int(maxPoints))
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
				points = make([]*infc.Point, 0, maxPoints)
				// Reset timer
				timer.Reset(time.Second * time.Duration(writeDataTimeout))
			}
			// Each point received saves its timestamp for use as a closing point
			// Don't use users data because it has aggregated time stamp instead of concrete one
			if p.Name() != "users" {
				lastPoint = p.Time()
			}
		// Await for external stop signal
		case <-ctx.Done():
			// Send any unsent points
			if len(points) > 0 {
				sendBatch(points)
				points = make([]*infc.Point, 0, int(maxPoints))
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
	p, _ := infc.NewPoint(
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
		// Add 5 secods to the time since last point was received
		lastPoint.Add(time.Second*5),
	)

	sendBatch([]*infc.Point{p})
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
func InitInfluxConnection(cmd *cobra.Command) error {
	username, _ := cmd.Flags().GetString("username")
	password, _ := cmd.Flags().GetString("password")
	address, _ := cmd.Flags().GetString("address")
	dbName, _ = cmd.Flags().GetString("database")
	maxPoints, _ = cmd.Flags().GetUint("max-batch-size")
	detached, _ := cmd.Flags().GetBool("detached")

	var err error
	c, err = infc.NewHTTPClient(infc.HTTPConfig{
		Addr:          address,
		Username:      username,
		Password:      password,
		UserAgent:     fmt.Sprintf("x2i-http-client-%s(%s)", cmd.Version, runtime.Version()),
		Timeout:       time.Second * 60,
		WriteEncoding: "gzip",
	})
	if err != nil {
		return err
	}
	//sleep for 15 seconds to allow influxdb to	start
	time.Sleep(15 * time.Second)

	_, _, err = c.Ping(time.Second * 10)
	if err != nil {
		return fmt.Errorf("Connection with InfluxDB at %s could not be established. Error: %w", address, err)
	}
	res, err := c.Query(infc.NewQuery("SHOW MEASUREMENTS", dbName, ""))
	if err != nil {
		return fmt.Errorf("Connection with InfluxDB at %s could not be established. Error: %w", address, err)
	}
	if err := res.Error(); err != nil {
		return fmt.Errorf("Test query failed with error: %w", err)
	}
	if !detached {
		l.Infof("Connection with InfluxDB at %s successfully established\n", address)
		return nil
	}

	return CloseDBConnection()
}

// CloseDBConnection just closes a connection to database when called
func CloseDBConnection() error {
	return c.Close()
}
