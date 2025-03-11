package gatlingparser

import (
	"context"
	"os"
	"testing"
	l "github.com/perfana/x2i/logger"
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
