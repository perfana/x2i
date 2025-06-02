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
	"encoding/binary"
	"errors" // Ensure errors is imported
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
	"golang.org/x/mod/semver"
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

	parserStopped = make(chan struct{})
)

func lookupTargetDir(ctx context.Context, dir string) error {
	const loopTimeout = 5 * time.Second

	cleanDir := filepath.Clean(filepath.FromSlash(dir))

	l.Infof("Looking for target directory... %s", cleanDir)
	for {
		select {
		case <-ctx.Done():
			return errStoppedByUser
		default:
		}

		fInfo, err := os.Stat(cleanDir)
		if err != nil {
			if os.IsNotExist(err) {
				time.Sleep(loopTimeout)
				continue
			}
			return fmt.Errorf("error accessing path %s (error type: %T): %w", dir, err, err)
		}

		if !fInfo.IsDir() {
			return fmt.Errorf("was expecting directory at %s, but found a file", dir)
		}

		abs, err := filepath.Abs(cleanDir)
		if err != nil {
			return fmt.Errorf("failed to get absolute path for %s: %w", dir, err)
		}

		l.Infof("Target directory found at %s", abs)
		break
	}

	return nil
}

func walkFunc(path string, info os.FileInfo, err error) error {
	if err != nil {
		l.Errorf("Error accessing path %s: %v", path, err)
		return nil
	}

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

func lookupResultsDir(ctx context.Context, dir string) error {
	const loopTimeout = 5 * time.Second

	l.Infof("Searching for results directory in %s...", dir)
	for {
		select {
		case <-ctx.Done():
			return errStoppedByUser
		default:
		}

		err := filepath.Walk(dir, walkFunc)
		if errors.Is(err, errFound) {
			break
		}
		if err != nil {
			return fmt.Errorf("failed to walk directory %s: %w", dir, err)
		}

		time.Sleep(loopTimeout)
	}

	return nil
}

func waitForLog(ctx context.Context) error {
	const loopTimeout = 5 * time.Second

	l.Infoln("Searching for " + simulationLogFileName + " file...")
	for {
		select {
		case <-ctx.Done():
			return errStoppedByUser
		default:
		}

		logFile := filepath.Join(logDir, simulationLogFileName)
		fInfo, err := os.Stat(logFile)
		if err != nil {
			if os.IsNotExist(err) {
				time.Sleep(loopTimeout)
				continue
			}
			return fmt.Errorf("failed to stat simulation log file %s: %w", logFile, err)
		}

		if fInfo.Size() < 300 {
			time.Sleep(loopTimeout)
			continue
		}

		abs, err := filepath.Abs(logFile)
		if err != nil {
			return fmt.Errorf("failed to get absolute path for %s: %w", logFile, err)
		}

		if !fInfo.Mode().IsRegular() {
			time.Sleep(loopTimeout)
			continue
		}

		isReadable := true
		if runtime.GOOS != "windows" {
			isReadable = fInfo.Mode().Perm()&0644 == 0644
		}

		if isReadable {
			l.Infof("Found log file at %s", abs)
			break
		}

		return errors.New("something wrong happened when attempting to open " + simulationLogFileName)
	}

	return nil
}

func timeFromUnixBytes(ub []byte) (time.Time, error) {
	timeStamp, err := strconv.ParseInt(string(ub), 10, 64)
	if err != nil {
		return time.Time{}, fmt.Errorf("Failed to parse timestamp as integer: %w", err)
	}
	return time.Unix(0, timeStamp*oneMillisecond+rand.Int63n(oneMillisecond)), nil
}

func userLineProcess(lb []byte) error {
	split := bytes.Split(lb, tabSep)
	if len(split) != 4 {
		return errors.New(fmt.Sprintf("USER line contains %d instead of 4 values", len(split)))
	}
	scenario := string(split[1])
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
		map[string]interface{}{"duration": int(end - start)},
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
		return fmt.Errorf("Failed to parse group start time: %w", err)
	}
	end, err := strconv.ParseInt(string(split[3]), 10, 64)
	if err != nil {
		return fmt.Errorf("Failed to parse group end time: %w", err)
	}
	rawDuration, err := strconv.ParseInt(string(split[4]), 10, 32)
	if err != nil {
		return fmt.Errorf("Failed to parse group raw duration: %w", err)
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
		map[string]interface{}{"totalDuration": int(end - start), "rawDuration": int(rawDuration)},
		timestamp,
	)
	if err != nil {
		return fmt.Errorf("Error creating new point with group data: %w", err)
	}
	influx.SendPoint(point)
	return nil
}

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

	influx.InitTestInfo(systemUnderTest, testEnvironment, simulationName, description, nodeName, testStartTime)
	point, err := influx.NewPoint(
		"tests",
		map[string]string{
			"action": "start", "simulation": simulationName,
			"systemUnderTest": systemUnderTest, "testEnvironment": testEnvironment, "nodeName": nodeName,
		},
		map[string]interface{}{"description": description},
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
			"systemUnderTest": systemUnderTest, "testEnvironment": testEnvironment,
			"nodeName": nodeName, "simulation": simulationName,
		},
		map[string]interface{}{"errorMessage": string(split[1])},
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
			return fmt.Errorf("%v: %w", err, errFatal)
		}
		return err
	default:
		if len(lineBuffer) > 24 {
			lineBuffer = lineBuffer[:24]
		}
		return fmt.Errorf("Unknown line type encountered: %s", lineBuffer)
	}
}

func detectGatlingLogVersion(file *os.File) (string, error) {
	defer func() {
		if _, err := file.Seek(0, 0); err != nil {
			l.Errorf("Failed to seek to beginning of file: %v", err)
		}
	}()
	var firstByte byte
	if err := binary.Read(file, currentByteOrder(), &firstByte); err != nil {
		if errors.Is(err, io.EOF) {
			return "", fmt.Errorf("file is empty")
		}
		// Check for ErrPartialRecord if binary.Read could return it (though less likely for a single byte)
		if errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, ErrPartialRecord) {
			return "", fmt.Errorf("file is truncated or header incomplete: %w", err)
		}
		return "", fmt.Errorf("failed to read first byte: %w", err)
	}

	if firstByte == 0 { // Binary format
		// Pass a new reader, as ReadRunMessage will consume bytes
		runMsg, err := DecodeRunMessage(bufio.NewReader(file)) // Changed to DecodeRunMessage
		if err != nil {
			// Check if the error is due to partial data (EOF / UnexpectedEOF wrapped in ErrPartialRecord)
			if errors.Is(err, ErrPartialRecord) {
				return "", fmt.Errorf("The file %s is empty or contains no readable header data (partial): %w", simulationLogFileName, err)
			}
			if errors.Is(err, io.EOF) { // Clean EOF after possibly reading some part of RunMessage
				return "", fmt.Errorf("The file %s is empty or contains no readable header data (EOF): %w", simulationLogFileName, err)
			}
			return "", err // Other errors
		}
		// If DecodeRunMessage succeeded, we need to reset the file offset for subsequent full header parsing
		if _, err := file.Seek(0, 0); err != nil {
			return "", fmt.Errorf("failed to seek to beginning of file after version detection: %w", err)
		}
		return runMsg.GatlingVersion, nil
	}

	// Text format
	if _, err := file.Seek(0, 0); err != nil {
		return "", fmt.Errorf("failed to seek to beginning of file: %w", err)
	}
	reader := bufio.NewReader(file)
	var line []byte
	var errRead error
	for {
		line, errRead = reader.ReadBytes('\n')
		if errRead != nil {
			return "", errRead
		}
		if runLine.Match(line) {
			break
		}
		if !bytes.HasPrefix(line, []byte("ASSERT")) {
			return "", errors.New("unexpected line before RUN line in text log")
		}
	}
	split := bytes.Split(line, tabSep)
	if len(split) != runLineLen {
		return "", errors.New("RUN line contains unexpected amount of values for text log")
	}
	return string(split[5]), nil
}

func fileProcessor(ctx context.Context, file *os.File) {
	r := bufio.NewReader(file)
	buf := new(bytes.Buffer)
	startWait := time.Now()

ParseLoop:
	for {
		select {
		case <-ctx.Done():
			l.Infoln("Parser received closing signal. Processing stopped")
			break ParseLoop
		default:
		}

		b, err := r.ReadBytes('\n')
		if err == io.EOF {
			if time.Now().After(startWait.Add(time.Duration(waitTime) * time.Second)) {
				l.Infof("No new lines found for %d seconds. Stopping application...", waitTime)
				break ParseLoop
			}
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
		buf.Reset()
		startWait = time.Now()
	}
	parserStopped <- struct{}{}
}

func processLogHeader(reader *bufio.Reader) (*RunMessage, []string, error) {
	var recordTypeByte byte
	err := binary.Read(reader, currentByteOrder(), &recordTypeByte)
	if err != nil {
		// Check for ErrPartialRecord if binary.Read could somehow lead to it (less likely for single byte)
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			return nil, nil, fmt.Errorf("processLogHeader: reading type byte: %w", ErrPartialRecord)
		}
		return nil, nil, err
	}
	// Use the constant from decoders.go (assuming it's defined there, or locally if not)
	if recordTypeByte != RunHeaderType {
		return nil, nil, fmt.Errorf("incorrect gatling log format: header record not found, got type %d", recordTypeByte)
	}

	runMessage, scenarios, _, err := ReadHeader(reader) // ReadHeader is in decoders.go
	if err != nil {
		// If ReadHeader returns ErrPartialRecord, propagate it
		if errors.Is(err, ErrPartialRecord) {
			// runMessage might be partially populated, return it along with error
			return &runMessage, scenarios, fmt.Errorf("processLogHeader: ReadHeader: %w", ErrPartialRecord)
		}
		return &runMessage, scenarios, err
	}

	l.Infof("Starting collecting for Gatling %s with simulation %s, that started at %s\n",
		runMessage.GatlingVersion, runMessage.SimulationClassName, time.UnixMilli(runMessage.Start))
	l.Infof("Scenarios %s\n", scenarios)
	return &runMessage, scenarios, nil
}

func processRemainingRecords(
	ctx context.Context,
	wg *sync.WaitGroup,
	reader *bufio.Reader,
	runMessage RunMessage,
	scenarios []string,
	records chan<- interface{},
	stopTimeout uint,
) {
	defer close(records)
	defer wg.Done()

	latestReadTime := time.Now()
	var recordCounter int = 0

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		if time.Now().After(latestReadTime.Add(time.Duration(stopTimeout) * time.Second)) {
			return
		}

		record, err := ReadNotHeaderRecord(reader, runMessage.Start, scenarios)

		if err == nil {
			recordCounter++
			select {
			case records <- record:
				latestReadTime = time.Now()
			case <-ctx.Done():
				return
			}
			continue
		}

		if errors.Is(err, ErrPartialRecord) {
			latestReadTime = time.Now() // Still trying for current record, count as activity
		} else if errors.Is(err, io.EOF) {
			// latestReadTime is NOT updated for clean EOF from RNHR's Peek. Timeout will be based on last actual success or partial try.
		} else {
			latestReadTime = time.Now() // Activity, trying to get past this error
		}

		time.Sleep(100 * time.Millisecond)
		continue
	}
}

type RecordsWriter interface {
	writeAll(wg *sync.WaitGroup, records <-chan interface{})
}

type InfluxRecordsWriter struct{}

func (w *InfluxRecordsWriter) writeAll(wg *sync.WaitGroup, records <-chan interface{}) {
	defer wg.Done()
	for record := range records {
		switch r := record.(type) {
		case RunMessage:
			simulationName = r.SimulationClassName[strings.LastIndex(r.SimulationClassName, ".")+1:]
			testStartTime := time.Unix(0, r.Start*oneMillisecond+rand.Int63n(oneMillisecond))
			influx.InitTestInfo(systemUnderTest, testEnvironment, simulationName, r.RunDescription, nodeName, testStartTime)
			point, err := r.ToInfluxPoint(testStartTime)
			if err != nil {
				l.Errorf("Error creating new point with test start data: %v", err)
			}
			influx.SendPoint(point)
		case RequestRecord:
			point, err := r.ToInfluxPoint()
			if err != nil {
				l.Errorf("Error creating new point with request data: %v", err)
			}
			influx.SendPoint(point)
		case GroupRecord:
			point, err := r.ToInfluxPoint()
			if err != nil {
				l.Errorf("Error creating new point with group data: %v", err)
			}
			influx.SendPoint(point)
		case UserRecord:
			timestamp, scenario, status := r.ToInfluxUserLineParams()
			influx.SendUserLineData(timestamp, scenario, status)
		case ErrorRecord:
			point, err := r.ToInfluxPoint()
			if err != nil {
				l.Errorf("Error creating new point with error data: %v", err)
			}
			influx.SendPoint(point)
		default:
			l.Errorf("Unknown record type: %T", r)
		}
	}
}

type SumRecordsWriter struct{}

func (w *SumRecordsWriter) writeAll(wg *sync.WaitGroup, records <-chan interface{}) {
	defer wg.Done()
	var (
		users, reqs, rumMessages, errors, groups int
	)
	for record := range records {
		switch record.(type) {
		case RunMessage:
			rumMessages++
		case RequestRecord:
			reqs++
		case GroupRecord:
			groups++
		case UserRecord:
			users++
		case ErrorRecord:
			errors++
		default:
			l.Errorf("Unknown record type: %T", record)
		}
	}
	l.Debugf("msg = %d, users = %d, reqs = %d, groups = %d, errors = %d\n", rumMessages, users, reqs, groups, errors)
}

func fileProcessorBinary(ctx context.Context, file *os.File, recordsWriter RecordsWriter) {
	defer func() { parserStopped <- struct{}{} }()
	reader := bufio.NewReader(file)

	runMessage, scenarios, err := processLogHeader(reader)
	if err != nil {
		if errors.Is(err, ErrPartialRecord) {
			l.Errorf("Log file %s reading error (possibly partial header): %v", file.Name(), err)
		} else {
			l.Errorf("Log file %s reading error: %v", file.Name(), err)
		}
		return
	}

	wg := &sync.WaitGroup{}
	records := make(chan interface{}, 100)
	records <- *runMessage

	wg.Add(2)
	go processRemainingRecords(ctx, wg, reader, *runMessage, scenarios, records, waitTime)
	go recordsWriter.writeAll(wg, records)
	wg.Wait()
}

func parseStart(ctx context.Context, wg *sync.WaitGroup) {
	defer wg.Done()

	l.Infoln("Starting log file parser...")
	file, err := os.Open(logDir + "/" + simulationLogFileName)
	if err != nil {
		l.Errorf("Failed to read %s file: %v\n", simulationLogFileName, err)
		return
	}
	defer func() {
		if err := file.Close(); err != nil {
			l.Errorf("Failed to close file: %v", err)
		}
	}()

	ver, err := detectGatlingLogVersion(file)
	if err != nil {
		l.Errorf("Failed to detect Gatling log version for %s: %v\n", simulationLogFileName, err)
		if errors.Is(err, ErrPartialRecord) {
			l.Errorln("File too short or header incomplete for version detection.")
		}
		return
	}

	if !semver.IsValid(ver) {
		ver = "v" + ver
	}
	if semver.Compare(ver, "v3.12.1") >= 0 {
		writer := InfluxRecordsWriter{}
		fileProcessorBinary(ctx, file, &writer)
	} else {
		fileProcessor(ctx, file)
	}
}

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
		return
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
		case <-cmd.Context().Done():
			pCancel()
		case <-parserStopped:
			iCancel()
			pCancel()
			break FinisherLoop
		}
	}
	wg.Wait()
}
