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

package gatlingparser

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/perfana/x2i/influx"
	l "github.com/perfana/x2i/logger"
	"github.com/spf13/cobra"
)

const (
	oneMillisecond        = 1_000_000
	simulationLogFileName = "simulation.log"
	// Constant amounts of elements per log line
	runLineLen     = 6
	requestLineLen = 8
	groupLineLen   = 7
	userLineLen    = 6
	errorLineLen   = 3
)

var (
	resultDirNamePattern = regexp.MustCompile(`^.+?-(\d{14})\d{3}$`)
	startTime            = time.Now().Unix()
	nodeName             string

	errFound         = errors.New("Found")
	errStoppedByUser = errors.New("Process stopped by user")
	errFatal         = errors.New("Fatal error")
	logDir           string
	systemUnderTest  string
	testEnvironment  string
	simulationName   string
	waitTime         uint

	tabSep = []byte{9}

	// regular expression patterns for matching log strings
	userLine    = regexp.MustCompile(`^USER\s`)
	requestLine = regexp.MustCompile(`^REQUEST\s`)
	groupLine   = regexp.MustCompile(`GROUP\s`)
	runLine     = regexp.MustCompile(`^RUN\s`)
	errorLine   = regexp.MustCompile(`^ERROR\s`)

	parserStopped        = make(chan struct{})
)

func lookupTargetDir(ctx context.Context, dir string) error {
	const loopTimeout = 5 * time.Second

	l.Infoln("Looking for target directory...")
	for {
		// This block checks if stop signal is received from user
		// and stops further lookup
		select {
		case <-ctx.Done():
			return errStoppedByUser
		default:
		}

		fInfo, err := os.Stat(dir)
		if err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("Target path %s exists but there is an error: %w", dir, err)
		}
		if os.IsNotExist(err) {
			time.Sleep(loopTimeout)
			continue
		}

		if !fInfo.IsDir() {
			return fmt.Errorf("Was expecting directory at %s, but found a file", dir)
		}

		abs, _ := filepath.Abs(dir)
		l.Infof("Target directory found at %s", abs)
		break
	}

	return nil
}

func walkFunc(path string, info os.FileInfo, err error) error {

	if info.IsDir() && resultDirNamePattern.MatchString(info.Name()) {
		l.Debugf("Found directory '%s' with mod time %s (start time: %s)", path, info.ModTime().String(), time.Unix(startTime, 0).String())
		startTimeMinusSlack := startTime - 60
		if info.ModTime().Unix() > startTimeMinusSlack {
			logDir = path
			l.Infof("Log directory '%s' with mod time %s is newer than start time minus slack %s", logDir,
				info.ModTime().String(),
				time.Unix(startTimeMinusSlack, 0).String())
			return errFound
		}
	}

	return nil
}

// logic is the following: at the start of the application current timestamp is saved
// then traversing over all directories inside target dir is initiated.
// Every dir name is matched against pattern, if found - modification time of directory is
// compared with application start time.
//
// This function stops as soon as directory modification time is after start time.
func lookupResultsDir(ctx context.Context, dir string) error {
	const loopTimeout = 5 * time.Second

	l.Infof("Searching for results directory...")
	for {
		// This block checks if stop signal is received from user
		// and stops further lookup
		select {
		case <-ctx.Done():
			return errStoppedByUser
		default:
		}

		err := filepath.Walk(dir, walkFunc)
		if err == errFound {
			break
		}
		if err != nil {
			return err
		}

		time.Sleep(loopTimeout)
	}

	return nil
}

func waitForLog(ctx context.Context) error {
	const loopTimeout = 5 * time.Second

	l.Infoln("Searching for " + simulationLogFileName + " file...")
	for {
		// This block checks if stop signal is received from user
		// and stops further lookup
		select {
		case <-ctx.Done():
			return errStoppedByUser
		default:
		}

		fInfo, err := os.Stat(logDir + "/" + simulationLogFileName)
		if err != nil && !os.IsNotExist(err) {
			return err
		}
		if os.IsNotExist(err) {
			time.Sleep(loopTimeout)
			continue
		}

		// WARNING: second part of this check may fail on Windows. Not tested
		if fInfo.Mode().IsRegular() && (runtime.GOOS == "windows" || fInfo.Mode().Perm() == 420) {
			abs, _ := filepath.Abs(logDir + "/" + simulationLogFileName)
			l.Infof("Found %s\n", abs)
			break
		}

		return errors.New("Something wrong happened when attempting to open " + simulationLogFileName)
	}

	return nil
}

func timeFromUnixBytes(ub []byte) (time.Time, error) {
	timeStamp, err := strconv.ParseInt(string(ub), 10, 64)
	if err != nil {
		return time.Time{}, fmt.Errorf("Failed to parse timestamp as integer: %w", err)
	}
	// A workaround that adds random amount of microseconds to the timestamp
	// so db entries will (should) not be overwritten
	return time.Unix(0, timeStamp*oneMillisecond+rand.Int63n(oneMillisecond)), nil
}

func userLineProcess(lb []byte) error {
	split := bytes.Split(lb, tabSep)

	splitCount := len(split)

	if splitCount != 4 {
		return errors.New(fmt.Sprintf("USER line contains %d instead of 4 values", splitCount))
	}
	scenario := string(split[1])
	// Using the second of the two timestamps
	// A user life duration may come in handy later
	timestamp, err := timeFromUnixBytes(bytes.TrimSpace(split[3]))
	if err != nil {
		return err
	}

	influx.SendUserLineData(timestamp, scenario, string(split[2]))

	return nil
}

func requestLineProcess(lb []byte) error {

	split := bytes.Split(lb, tabSep)
	if len(split) != 7 {
		return errors.New("REQUEST line contains unexpected amount of values")
	}

	start, err := strconv.ParseInt(string(split[3]), 10, 64)
	if err != nil {
		return fmt.Errorf("Failed to parse request start time in line as integer: %w", err)
	}
	end, err := strconv.ParseInt(string(split[4]), 10, 64)
	if err != nil {
		return fmt.Errorf("Failed to parse request end time in line as integer: %w", err)
	}
	timestamp, err := timeFromUnixBytes(split[4])
	if err != nil {
		return err
	}

	point, err := influx.NewPoint(
		"requests",
		map[string]string{
			"name":            strings.TrimSpace(strings.ReplaceAll(string(split[2]), " ", "_")),
			"groups":          strings.TrimSpace(strings.ReplaceAll(string(split[1]), " ", "_")),
			"result":          string(split[5]),
			"simulation":      simulationName,
			"systemUnderTest": systemUnderTest,
			"testEnvironment": testEnvironment,
			"nodeName":        nodeName,
			"errorMessage":    string(bytes.TrimSpace(split[6])),
		},
		map[string]interface{}{
			"duration": int(end - start),
		},
		timestamp,
	)
	if err != nil {
		return fmt.Errorf("Error creating new point with request data: %w", err)
	}

	influx.SendPoint(point)

	return nil
}

func groupLineProcess(lb []byte) error {
	split := bytes.Split(lb, tabSep)

	if len(split) != 6 {
		return errors.New("GROUP line contains unexpected amount of values")
	}

	start, err := strconv.ParseInt(string(split[2]), 10, 64)
	if err != nil {
		return fmt.Errorf("Failed to parse group start time in line as integer: %w", err)
	}
	end, err := strconv.ParseInt(string(split[3]), 10, 64)
	if err != nil {
		return fmt.Errorf("Failed to parse group end time in line as integer: %w", err)
	}
	rawDuration, err := strconv.ParseInt(string(split[4]), 10, 32)
	if err != nil {
		return fmt.Errorf("Failed to parse group raw duration in line as integer: %w", err)
	}
	timestamp, err := timeFromUnixBytes(split[3])
	if err != nil {
		return err
	}

	point, err := influx.NewPoint(
		"groups",
		map[string]string{
			"name":            strings.TrimSpace(strings.ReplaceAll(string(split[1]), " ", "_")),
			"result":          string(split[5][:2]),
			"simulation":      simulationName,
			"systemUnderTest": systemUnderTest,
			"testEnvironment": testEnvironment,
			"nodeName":        nodeName,
		},
		map[string]interface{}{
			"totalDuration": int(end - start),
			"rawDuration":   int(rawDuration),
		},
		timestamp,
	)
	if err != nil {
		return fmt.Errorf("Error creating new point with group data: %w", err)
	}

	influx.SendPoint(point)

	return nil
}

// This method should be called first when parsing started as it is based
// on information from the header row
func runLineProcess(lb []byte) error {
	split := bytes.Split(lb, tabSep)
	if len(split) != runLineLen {
		return errors.New("RUN line contains unexpected amount of values")
	}

	simulationName = string(split[1])[strings.LastIndex(string(split[1]), ".")+1:]
	description := string(split[4])
	testStartTime, err := timeFromUnixBytes(split[3])
	if err != nil {
		return err
	}

	// This will initialize required data for influx client
	influx.InitTestInfo(systemUnderTest, testEnvironment, simulationName, description, nodeName, testStartTime)

	point, err := influx.NewPoint(
		"tests",
		map[string]string{
			"action":          "start",
			"simulation":      simulationName,
			"systemUnderTest": systemUnderTest,
			"testEnvironment": testEnvironment,
			"nodeName":        nodeName,
		},
		map[string]interface{}{
			"description": description,
		},
		testStartTime,
	)
	if err != nil {
		return fmt.Errorf("Error creating new point with test start data: %w", err)
	}

	influx.SendPoint(point)

	return nil
}

func errorLineProcess(lb []byte) error {
	split := bytes.Split(lb, tabSep)
	if len(split) != errorLineLen {
		return errors.New("ERROR line contains unexpected amount of values")
	}
	timestamp, err := timeFromUnixBytes(bytes.TrimSpace(split[2]))
	if err != nil {
		return err
	}

	point, err := influx.NewPoint(
		"errors",
		map[string]string{
			"systemUnderTest": systemUnderTest,
			"testEnvironment": testEnvironment,
			"nodeName":        nodeName,
			"simulation":      simulationName,
		},
		map[string]interface{}{
			"errorMessage": string(split[1]),
		},
		timestamp,
	)
	if err != nil {
		return fmt.Errorf("Error creating new point with error data: %w", err)
	}

	influx.SendPoint(point)

	return nil
}

func stringProcessor(lineBuffer []byte) error {

	switch {
	case requestLine.Match(lineBuffer):
		return requestLineProcess(lineBuffer)
	case groupLine.Match(lineBuffer):
		return groupLineProcess(lineBuffer)
	case userLine.Match(lineBuffer):
		return userLineProcess(lineBuffer)
	case errorLine.Match(lineBuffer):
		return errorLineProcess(lineBuffer)
	case runLine.Match(lineBuffer):
		err := runLineProcess(lineBuffer)
		if err != nil {
			// Wrapping in a fatal error because further processing is futile
			err = fmt.Errorf("%v: %w", err, errFatal)
		}
		return err
	default:
		return fmt.Errorf("Unknown line type encountered")
	}
}

func fileProcessor(ctx context.Context, file *os.File) {
	r := bufio.NewReader(file)
	buf := new(bytes.Buffer)
	startWait := time.Now()

ParseLoop:
	for {
		// This block checks if stop signal is received from user
		// and stops further processing
		select {
		case <-ctx.Done():
			l.Infoln("Parser received closing signal. Processing stopped")
			break ParseLoop
		default:
		}

		b, err := r.ReadBytes('\n')
		if err == io.EOF {
			// If no new lines read for more than value provided by 'stop-timeout' key then processing is stopped
			if time.Now().After(startWait.Add(time.Duration(waitTime) * time.Second)) {
				l.Infof("No new lines found for %d seconds. Stopping application...", waitTime)
				break ParseLoop
			}
			// All new data is stored in buffer until next loop
			buf.Write(b)
			time.Sleep(time.Second)
			continue
		}
		if err != nil {
			l.Errorf("Unexpected error encountered while parsing file: %v", err)
		}

		buf.Write(b)
		err = stringProcessor(buf.Bytes())
		if err != nil {
			l.Errorf("String processing failed: %v", err)
			if errors.Is(err, errFatal) {
				l.Errorln("Log parser caught an error that can't be handled. Stopping application...")
				break ParseLoop
			}
		}
		// Clean buffer after processing preparing for a new loop
		buf.Reset()
		// Reset a timeout timer
		startWait = time.Now()
	}
	parserStopped <- struct{}{}
}

func parseStart(ctx context.Context, wg *sync.WaitGroup) {
	defer wg.Done()

	l.Infoln("Starting log file parser...")
	file, err := os.Open(logDir + "/" + simulationLogFileName)
	if err != nil {
		l.Errorf("Failed to read %s file: %v\n", simulationLogFileName, err)
	}
	defer file.Close()

	fileProcessor(ctx, file)
}

// RunMain performs main application logic
func RunMain(cmd *cobra.Command, dir string) {
	systemUnderTest, _ = cmd.Flags().GetString("system-under-test")
	testEnvironment, _ = cmd.Flags().GetString("test-environment")
	waitTime, _ = cmd.Flags().GetUint("stop-timeout")
	rand.Seed(time.Now().UnixNano())
	nodeName, _ = os.Hostname()

	l.Infof("Searching for directory at %s", dir)
	abs, err := filepath.Abs(dir)
	if err != nil {
		l.Errorf("Failed to construct an absolute path for %s: %v", dir, err)
	}

	if err := lookupTargetDir(cmd.Context(), abs); err != nil {
		if err == errStoppedByUser {
			return
		}
		l.Errorf("Target directory lookup failed with error: %v\n", err)
		os.Exit(1)
	}

	if err := lookupResultsDir(cmd.Context(), abs); err != nil {
		if err == errStoppedByUser {
			return
		}
		l.Errorf("Error happened while searching for results directory: %v\n", err)
		os.Exit(1)
	}

	if err := waitForLog(cmd.Context()); err != nil {
		if err == errStoppedByUser {
			return
		}
		l.Errorf("Failed waiting for %s with error: %v\n", simulationLogFileName, err)
		os.Exit(1)
	}

	wg := &sync.WaitGroup{}
	pCtx, pCancel := context.WithCancel(context.Background())
	iCtx, iCancel := context.WithCancel(context.Background())

	wg.Add(2)
	go parseStart(pCtx, wg)
	go influx.StartProcessing(iCtx, wg)

FinisherLoop:
	for {
		select {
		// If top level context is cancelled we first stop the parser
		case <-cmd.Context().Done():
			pCancel()
		// Then wait for parser to stop and stop client processing
		case <-parserStopped:
			iCancel()
			// In case parser finished processing on its own, we cancel its context
			pCancel()
			break FinisherLoop
		}
	}
	wg.Wait()
}
