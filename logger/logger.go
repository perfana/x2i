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

package logger

import (
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
)

type LogLevel int

const (
	INFO LogLevel = iota
	ERROR
	DEBUG
)

var (
	logger    *log.Logger
	sw        io.Writer
	ew        io.Writer
	logLevel  LogLevel = INFO // Default to INFO level
)

// InitLogger sets up a new instance of logger that writes to file and STDOUT
func InitLogger(fileName string) error {
	p, err := filepath.Abs(fileName)
	if err != nil {
		return fmt.Errorf("Failed to build absolute path for log file: %w", err)
	}
	dir := filepath.Dir(p)
	err = os.MkdirAll(dir, 0755)
	if err != nil {
		return fmt.Errorf("Failed to create log directory: %w", err)
	}

	file, err := os.OpenFile(fileName, os.O_WRONLY|os.O_CREATE|os.O_APPEND|os.O_SYNC, 0644)
	if err != nil {
		return fmt.Errorf("Cannot create log file at %s: %w", fileName, err)
	}
	sw = io.MultiWriter(os.Stdout, file)
	ew = io.MultiWriter(os.Stderr, file)
	logger = log.New(sw, "", log.Ldate|log.Ltime|log.LUTC)

	return nil
}

func SetLogLevel(level LogLevel) {
	logLevel = level
}

func Errorln(v ...interface{}) {
	if logLevel >= ERROR {
		logger.SetOutput(ew)
		logger.SetPrefix("ERROR ")
		logger.Println(v...)
	}
}

func Errorf(format string, v ...interface{}) {
	if logLevel >= ERROR {
		logger.SetOutput(ew)
		logger.SetPrefix("ERROR ")
		logger.Printf(format, v...)
	}
}
func Infoln(v ...interface{}) {
	if logLevel >= INFO {
		logger.SetOutput(sw)
		logger.SetPrefix("INFO ")
		logger.Println(v...)
	}
}

func Infof(format string, v ...interface{}) {
	if logLevel >= INFO {
		logger.SetOutput(sw)
		logger.SetPrefix("INFO ")
		logger.Printf(format, v...)
	}
}

func Debugln(v ...interface{}) {
	if logLevel >= DEBUG {
		logger.SetOutput(sw)
		logger.SetPrefix("DEBUG ")
		logger.Println(v...)
	}
}

func Debugf(format string, v ...interface{}) {
	if logLevel >= DEBUG {
		logger.SetOutput(sw)
		logger.SetPrefix("DEBUG ")
		logger.Printf(format, v...)
	}
}