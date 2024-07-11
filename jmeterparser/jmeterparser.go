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

package jmeterparser

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
	"runtime"
	"strconv"
	"sync"
	"time"
	"strings"

	"github.com/perfana/x2i/influx"
	l "github.com/perfana/x2i/logger"
	"github.com/spf13/cobra"
)

const (
	oneMillisecond        = 1_000_000
)

var (
	nodeName         string
	resultsLogFileName string
	errStoppedByUser = errors.New("Process stopped by user")
	errFatal         = errors.New("Fatal error")
	logDir           string
	systemUnderTest  string
	testEnvironment  string
	waitTime         uint
	parserStopped = make(chan struct{})
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
		logDir = abs
		break
	}

	return nil
}

func waitForLog(ctx context.Context) error {

	const loopTimeout = 5 * time.Second
    const resultFilePattern = "*.csv"

	l.Infoln("Searching for " + logDir + "/" + resultFilePattern + " files...")
	for {
		// This block checks if stop signal is received from user
		// and stops further lookup
		select {
		case <-ctx.Done():
			return errStoppedByUser
		default:
		}

        files, err := filepath.Glob(logDir + "/" + resultFilePattern)
        if err != nil {
            fmt.Println("Error:", err)
            return err
        }
        if len(files) == 0 {
            fmt.Println("No results file found in dir %s matching pattern %s", logDir, resultFilePattern)
			time.Sleep(loopTimeout)
            continue
        }
        resultsLogFileName = filepath.Base(files[0])

		fInfo, err := os.Stat(logDir + "/" + resultsLogFileName)
		if err != nil && !os.IsNotExist(err) {
			return err
		}
		if os.IsNotExist(err) {
			time.Sleep(loopTimeout)
			continue
		}

		// WARNING: second part of this check may fail on Windows. Not tested
		if fInfo.Mode().IsRegular() && (runtime.GOOS == "windows" || fInfo.Mode().Perm() == 420) {
			abs, _ := filepath.Abs(logDir + "/" + resultsLogFileName)
			l.Infof("Found %s\n", abs)
			break
		}

		return errors.New("Something wrong happened when attempting to open " + resultsLogFileName)
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

func requestLineProcess(lb []byte) error {

	split := bytes.Split(lb, []byte(","))
		if len(split) != 17 {
			return errors.New("Line contains unexpected amount of values")
		}

		timestamp, err := timeFromUnixBytes(split[0])
		if err != nil {
			return err
		}

		duration, err := strconv.Atoi(string(split[1]))
		if err != nil {
			fmt.Println("Error:", err)
			return err
		}
		grpThreads, err := strconv.Atoi(string(split[11]))
		if err != nil {
			fmt.Println("Error:", err)
			return err
		}
		allThreads, err := strconv.Atoi(string(split[12]))
		if err != nil {
			fmt.Println("Error:", err)
			return err
		}
		requestPoint, err := influx.NewPoint(
				"requests",
				map[string]string{
					"label":       strings.TrimSpace(strings.ReplaceAll(string(split[2]), " ", "_")),
					"success":     string(split[7]),
					"systemUnderTest": systemUnderTest,
					"testEnvironment": testEnvironment,
					"nodeName":   nodeName,
					"responseCode": string(split[3]),
					"grpThreads": string(split[11]),
					"allThreads": string(split[12]),
					"failureMessage": string(bytes.TrimSpace(split[8])),
				},
				map[string]interface{}{
					"duration":  duration   ,
				},
				timestamp,
			)
			if err != nil {
				return fmt.Errorf("Error creating new point with request data: %w", err)
			}

			influx.SendPoint(requestPoint)

		grpThreadsPoint, err := influx.NewPoint(
				"groupThreads",
				map[string]string{
					"success":     string(split[7]),
					"threadName":     string(split[5]),
					"systemUnderTest": systemUnderTest,
					"testEnvironment": testEnvironment,
					"nodeName":   nodeName,
				},
				map[string]interface{}{
					"grpThreads":  grpThreads ,
				},
				timestamp,
			)
			if err != nil {
				return fmt.Errorf("Error creating new point with request data: %w", err)
			}

			influx.SendPoint(grpThreadsPoint)

		allThreadsPoint, err := influx.NewPoint(
				"allThreads",
				map[string]string{
					"success":     string(split[7]),
					"systemUnderTest": systemUnderTest,
					"testEnvironment": testEnvironment,
					"nodeName":   nodeName,
				},
				map[string]interface{}{
					"allThreads":  allThreads ,
				},
				timestamp,
			)
			if err != nil {
				return fmt.Errorf("Error creating new point with request data: %w", err)
			}

			influx.SendPoint(allThreadsPoint)

	return nil
}

func stringProcessor(lineBuffer []byte) error {

    // todo: the first line contains the header, e.g.
    // timeStamp,elapsed,label,responseCode, etc.
    // this line should be skipped, currently it causes an error in the log file, but does not harm:
    // ERROR 2024/07/10 10:47:06 String processing failed: Failed to parse timestamp as integer:
    // strconv.ParseInt: parsing "timeStamp": invalid syntax
 	return requestLineProcess(lineBuffer)

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
	file, err := os.Open(logDir + "/" + resultsLogFileName)
	if err != nil {
		l.Errorf("Failed to read %s file: %v\n", resultsLogFileName, err)
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

	if err := waitForLog(cmd.Context()); err != nil {
		if err == errStoppedByUser {
			return
		}
		l.Errorf("Failed waiting for %s with error: %v\n", resultsLogFileName, err)
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