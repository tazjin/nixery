package main

// This file configures different log formatters via logrus. The
// standard formatter uses a structured JSON format that is compatible
// with Stackdriver Error Reporting.
//
// https://cloud.google.com/error-reporting/docs/formatting-error-messages

import (
	"bytes"
	"encoding/json"
	log "github.com/sirupsen/logrus"
)

type stackdriverFormatter struct{}

type serviceContext struct {
	Service string `json:"service"`
	Version string `json:"version"`
}

type reportLocation struct {
	FilePath     string `json:"filePath"`
	LineNumber   int    `json:"lineNumber"`
	FunctionName string `json:"functionName"`
}

var nixeryContext = serviceContext{
	Service: "nixery",
}

// isError determines whether an entry should be logged as an error
// (i.e. with attached `context`).
//
// This requires the caller information to be present on the log
// entry, as stacktraces are not available currently.
func isError(e *log.Entry) bool {
	l := e.Level
	return (l == log.ErrorLevel || l == log.FatalLevel || l == log.PanicLevel) &&
		e.HasCaller()
}

func (f stackdriverFormatter) Format(e *log.Entry) ([]byte, error) {
	msg := e.Data
	msg["serviceContext"] = &nixeryContext
	msg["message"] = &e.Message
	msg["eventTime"] = &e.Time

	if isError(e) {
		loc := reportLocation{
			FilePath: e.Caller.File,
			LineNumber: e.Caller.Line,
			FunctionName: e.Caller.Function,
		}
		msg["context"] = &loc
	}

	b := new(bytes.Buffer)
	err := json.NewEncoder(b).Encode(&msg)

	return b.Bytes(), err
}

func init() {
	nixeryContext.Version = version
	log.SetReportCaller(true)
	log.SetFormatter(stackdriverFormatter{})
}
