package gatlingparser

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	l "github.com/perfana/x2i/logger"
	// "github.com/stretchr/testify/assert" // Would be useful for assertions
)

func TestFileProcessorBinary(t *testing.T) {
	l.InitLogger("testlog.log")
	waitTime = 10
	ctx := context.Background()
	
	file, err := os.Open("../test/data/simulation.log")
	if err != nil {
		t.Errorf("os.Open(%v) returned error: %v", ctx, err)
	}
	go func ()  {
		 <- parserStopped
	}()

	writer := SumRecordsWriter{}
	
	fileProcessorBinary(ctx, file, &writer)
}

// CountingWriter for tallying records processed.
type CountingWriter struct {
	RunMessages int
	Users       int
	Requests    int
	Groups      int
	Errors      int
	mu          sync.Mutex
}

// writeAll satisfies the RecordsWriter interface for testing.
func (cw *CountingWriter) writeAll(wg *sync.WaitGroup, records <-chan interface{}) {
	if wg != nil {
		defer wg.Done()
	}
	for record := range records {
		cw.mu.Lock()
		switch record.(type) {
		case RunMessage:
			cw.RunMessages++
		case UserRecord:
			cw.Users++
		case RequestRecord:
			cw.Requests++
		case GroupRecord:
			cw.Groups++
		case ErrorRecord:
			cw.Errors++
		default:
			// Potentially log unexpected type, though test focuses on knowns
		}
		cw.mu.Unlock()
	}
}

// Helper to get expected counts by parsing the static file once.
func getExpectedCounts(t *testing.T, logPath string) CountingWriter {
	t.Helper()
	file, err := os.Open(logPath)
	if err != nil {
		t.Fatalf("getExpectedCounts: Failed to open source log '%s': %v", logPath, err)
	}
	defer file.Close()

	reader := bufio.NewReader(file)
	runMessage, scenarios, err := processLogHeader(reader)
	if err != nil {
		t.Fatalf("getExpectedCounts: Failed to process header from '%s': %v", logPath, err)
	}

	var counts CountingWriter
	counts.RunMessages++ // Account for the header RunMessage

	for {
		record, err := ReadNotHeaderRecord(reader, runMessage.Start, scenarios)
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("getExpectedCounts: Error reading record from '%s': %v", logPath, err)
		}
		switch record.(type) {
		case UserRecord:
			counts.Users++
		case RequestRecord:
			counts.Requests++
		case GroupRecord:
			counts.Groups++
		case ErrorRecord:
			counts.Errors++
		}
	}
	t.Logf("getExpectedCounts: %+v from %s", counts, logPath)
	return counts
}

func TestFileProcessorBinary_ConcurrentWrite(t *testing.T) {
	l.InitLogger("testlog_concurrent.log")
	originalWaitTime := waitTime
	waitTime = 5 // seconds, for parser's internal timeout
	defer func() { waitTime = originalWaitTime }()

	sourceLogPath := "../test/data/simulation.log"
	if _, err := os.Stat(sourceLogPath); os.IsNotExist(err) {
		t.Fatalf("Source log file does not exist: %s", sourceLogPath)
	}

	tempDir := t.TempDir()
	tempLogPath := filepath.Join(tempDir, "simulation_concurrent.log")

	expectedCounts := getExpectedCounts(t, sourceLogPath)

	var writerWg, parserWg sync.WaitGroup
	headerReadyChan := make(chan struct{})
	writerErrorChan := make(chan error, 1)
	parserErrorChan := make(chan error, 1)

	writerWg.Add(1)
	go func() {
		defer writerWg.Done()
		defer func() {
			if r := recover(); r != nil {
				writerErrorChan <- fmt.Errorf("writer panic: %v", r)
			}
		}()

		sourceFile, err := os.Open(sourceLogPath)
		if err != nil {
			writerErrorChan <- fmt.Errorf("writer: Failed to open source log: %v", err)
			close(headerReadyChan)
			return
		}
		defer sourceFile.Close()

		tempFile, err := os.OpenFile(tempLogPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)
		if err != nil {
			writerErrorChan <- fmt.Errorf("writer: Failed to create temp log: %v", err)
			close(headerReadyChan)
			return
		}
		defer tempFile.Close()

		buffer := make([]byte, 128)
		firstChunkWritten := false
		totalBytesWritten := 0

		for {
			n, errRead := sourceFile.Read(buffer)
			if n > 0 {
				writtenN, errWrite := tempFile.Write(buffer[:n])
				if errWrite != nil {
					writerErrorChan <- fmt.Errorf("writer: Failed to write chunk to temp log: %v", errWrite)
					return
				}
				if writtenN != n {
					writerErrorChan <- fmt.Errorf("writer: Partial write to temp log. Expected %d, wrote %d", n, writtenN)
					return
				}
				totalBytesWritten += n
				t.Logf("Writer: Wrote %d bytes (total %d).", n, totalBytesWritten)

				if !firstChunkWritten {
					t.Log("Writer: First chunk written, signaling header (approximated).")
					close(headerReadyChan)
					firstChunkWritten = true
				}
				time.Sleep(20 * time.Millisecond)
			}
			if errRead == io.EOF {
				t.Log("Writer: EOF reached on source log.")
				break
			}
			if errRead != nil {
				writerErrorChan <- fmt.Errorf("writer: Failed to read chunk from source log: %v", errRead)
				return
			}
		}
		if !firstChunkWritten && totalBytesWritten == 0 {
			close(headerReadyChan)
		}
		t.Logf("Writer goroutine finished writing %d bytes.", totalBytesWritten)
	}()

	parserWg.Add(1)
	var parsedCounts CountingWriter
	go func() {
		defer parserWg.Done()
		defer func() {
			if r := recover(); r != nil {
				parserErrorChan <- fmt.Errorf("parser panic: %v", r)
			}
		}()

		t.Log("Parser: Waiting for header ready signal (first chunk written)...")
		select {
		case <-headerReadyChan:
			t.Log("Parser: Header ready signal received.")
		case <-time.After(10 * time.Second):
			parserErrorChan <- fmt.Errorf("parser: Timeout waiting for headerReady signal")
			return
		case err := <-writerErrorChan:
			if err != nil {
				parserErrorChan <- fmt.Errorf("parser: Writer failed before header: %v", err)
				return
			}
			t.Log("Parser: Writer finished (or sent nil error) before header signal processed by parser.")
		}

		var tempFileForParser *os.File
		var errOpen error
		for i := 0; i < 30; i++ {
			tempFileForParser, errOpen = os.Open(tempLogPath)
			if errOpen == nil {
				break
			}
			time.Sleep(100 * time.Millisecond)
		}
		if errOpen != nil {
			parserErrorChan <- fmt.Errorf("parser: Failed to open temp log '%s' after retries: %v", tempLogPath, errOpen)
			return
		}
		defer tempFileForParser.Close()
		t.Logf("Parser: Successfully opened temp log '%s'", tempLogPath)

		parserContext, cancelParser := context.WithTimeout(context.Background(), 25*time.Second)
		defer cancelParser()

		select {
		case <-parserStopped:
			t.Log("Parser: Drained a previously closed parserStopped signal.")
		default:
		}

		// Drain any signal from a PREVIOUS test run.
		select {
		case <-parserStopped:
			t.Log("Parser: Drained a previously closed parserStopped signal (pre-run).")
		default:
		}

		// Launch a goroutine to ensure parserStopped is drained, allowing
		// fileProcessorBinary's deferred send to complete.
		drainDone := make(chan struct{})
		go func() {
			defer close(drainDone)
			select {
			case <-parserStopped:
				t.Log("Parser: Helper goroutine consumed parserStopped signal.")
			case <-time.After(6 * time.Second): // Should be > waitTime + processing buffer
				// If this timeout hits, fileProcessorBinary's defer did not send on parserStopped
				t.Error("Parser: Helper goroutine timed out waiting for parserStopped signal.")
				// This error will be caught by the main test logic if it causes test failure.
			}
		}()

		fileProcessorBinary(parserContext, tempFileForParser, &parsedCounts)
		t.Log("Parser: fileProcessorBinary execution completed.")

		// Wait for the drain helper to complete to ensure the signal was processed (or timed out)
		<-drainDone
		t.Log("Parser: Helper goroutine for draining parserStopped finished.")
	}()

	overallTestTimeoutDuration := 30 * time.Second
	overallTestTimeout := time.After(overallTestTimeoutDuration)

	waitForGoroutine := func(t *testing.T, wg *sync.WaitGroup, name string, errorChan <-chan error, testTimeout <-chan time.Time) bool {
		t.Helper()
		done := make(chan struct{})
		go func() {
			wg.Wait()
			close(done)
		}()
		select {
		case <-done:
			t.Logf("Test: %s goroutine complete.", name)
			select {
			case err, ok := <-errorChan:
				if ok && err != nil {
					t.Errorf("Test: %s goroutine failed: %v", name, err)
					return false
				}
			default:
			}
			return true
		case err, ok := <-errorChan:
			if ok && err != nil {
				t.Errorf("Test: %s goroutine failed during execution: %v", name, err)
				return false
			}
			return true
		case <-testTimeout:
			t.Errorf("Test: Timeout waiting for %s goroutine to complete.", name)
			return false
		}
	}

	if !waitForGoroutine(t, &writerWg, "Writer", writerErrorChan, overallTestTimeout) {
		t.FailNow()
	}
	if !waitForGoroutine(t, &parserWg, "Parser", parserErrorChan, overallTestTimeout) {
		t.FailNow()
	}

	select {
	case err, ok := <-writerErrorChan:
		if ok && err != nil {
			t.Fatalf("Writer goroutine reported a late error: %v", err)
		}
	default:
	}
	select {
	case err, ok := <-parserErrorChan:
		if ok && err != nil {
			t.Errorf("Parser goroutine reported a late error: %v", err)
		}
	default:
	}

	if t.Failed() {
		t.Log("Test failed due to errors in goroutines or timeouts. Skipping count assertions.")
		return
	}

	if expectedCounts.RunMessages != parsedCounts.RunMessages {
		t.Errorf("RunMessages: expected %d, got %d", expectedCounts.RunMessages, parsedCounts.RunMessages)
	}
	if expectedCounts.Users != parsedCounts.Users {
		t.Errorf("Users: expected %d, got %d", expectedCounts.Users, parsedCounts.Users)
	}
	if expectedCounts.Requests != parsedCounts.Requests { // Corrected this line
		t.Errorf("Requests: expected %d, got %d", expectedCounts.Requests, parsedCounts.Requests)
	}
	if expectedCounts.Groups != parsedCounts.Groups {
		t.Errorf("Groups: expected %d, got %d", expectedCounts.Groups, parsedCounts.Groups)
	}
	if expectedCounts.Errors != parsedCounts.Errors {
		t.Errorf("Errors: expected %d, got %d", expectedCounts.Errors, parsedCounts.Errors)
	}

	t.Logf("Test TestFileProcessorBinary_ConcurrentWrite completed. Expected: %+v, Parsed: %+v", expectedCounts, parsedCounts)
}
