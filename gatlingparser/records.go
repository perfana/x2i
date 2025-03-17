package gatlingparser

import (
	"fmt"
	client "github.com/influxdata/influxdb1-client/v2"
	"github.com/perfana/x2i/influx"
	"math/rand"
	"strings"
	"time"
)

type RunMessage struct {
	SimulationClassName string
	SimulationId        string
	Start               int64
	RunDescription      string
	GatlingVersion      string
}

func (rm RunMessage) String() string {
	return fmt.Sprintf("Gatling log:\n\tversion: %s\n\tclassName: %s\n\tstart: %s\n", rm.GatlingVersion, rm.SimulationClassName, time.UnixMilli(rm.Start))
}

func (rm RunMessage) ToInfluxPoint(testStartTime time.Time) (*client.Point, error) {
	return influx.NewPoint(
		"tests",
		map[string]string{
			"action":          "start",
			"simulation":      simulationName,
			"systemUnderTest": systemUnderTest,
			"testEnvironment": testEnvironment,
			"nodeName":        nodeName,
		},
		map[string]interface{}{
			"description": rm.RunDescription,
		},
		testStartTime,
	)
}

type RequestRecord struct {
	Group          *Group
	Name           string
	Status         bool
	StartTimestamp int64
	EndTimestamp   int64
	ResponseTime   int32
	ErrorMessage   *string
	Incoming       bool
}

func (rr RequestRecord) String() string {
	v := "KO"
	if rr.Status {
		v = "OK"
	}
	return fmt.Sprintf("REQ group: %s, name: %s, start: %s, end: %s, status: %s",
		*rr.Group,
		rr.Name,
		time.UnixMilli(rr.StartTimestamp),
		time.UnixMilli(rr.EndTimestamp),
		v,
	)
}

func toInfluxTimestamp(millis int64) time.Time {
	return time.Unix(0, millis*oneMillisecond+rand.Int63n(oneMillisecond))
}

func statusToString(status bool) string {
	statusString := "KO"
	if status {
		statusString = "OK"
	}
	return statusString
}

func (rr RequestRecord) ToInfluxPoint() (*client.Point, error) {
	timestamp := toInfluxTimestamp(rr.EndTimestamp)
	groupString := strings.TrimSpace(strings.Join(rr.Group.Hierarchy, "_"))
	statusString := statusToString(rr.Status)
	errorMessage := ""
	if rr.ErrorMessage != nil {
		errorMessage = *rr.ErrorMessage
	}

	return influx.NewPoint(
		"requests",
		map[string]string{
			"name":            strings.TrimSpace(strings.ReplaceAll(rr.Name, " ", "_")),
			"groups":          groupString,
			"result":          statusString,
			"simulation":      simulationName,
			"systemUnderTest": systemUnderTest,
			"testEnvironment": testEnvironment,
			"nodeName":        nodeName,
			"errorMessage":    errorMessage,
		},
		map[string]interface{}{
			"duration": int(rr.EndTimestamp - rr.StartTimestamp),
		},
		timestamp,
	)
}

type GroupRecord struct {
	Group                 Group
	Duration              int32
	CumulatedResponseTime int32
	Status                bool
	StartTimestamp        int64
	EndTimestamp          int64
}

func (gr GroupRecord) ToInfluxPoint() (*client.Point, error) {
	timestamp := toInfluxTimestamp(gr.EndTimestamp)
	groupString := strings.TrimSpace(strings.Join(gr.Group.Hierarchy, "_"))
	statusString := statusToString(gr.Status)

	return influx.NewPoint(
		"groups",
		map[string]string{
			"name":            groupString,
			"result":          statusString,
			"simulation":      simulationName,
			"systemUnderTest": systemUnderTest,
			"testEnvironment": testEnvironment,
			"nodeName":        nodeName,
		},
		map[string]interface{}{
			"totalDuration": int(gr.EndTimestamp - gr.StartTimestamp),
			"rawDuration":   int(gr.CumulatedResponseTime),
		},
		timestamp,
	)
}

func (gr GroupRecord) String() string {
	return fmt.Sprintf("GRO group: %s, start: %s, end: %s, cumulatedResponseTime: %d",
		gr.Group,
		time.UnixMilli(gr.StartTimestamp),
		time.UnixMilli(gr.EndTimestamp),
		gr.CumulatedResponseTime,
	)
}

type UserRecord struct {
	Scenario  string
	Event     bool
	Timestamp int64
}

func eventToSting(event bool) string {
	eventString := "END"
	if event {
		eventString = "START"
	}
	return eventString
}

func (ur UserRecord) String() string {
	event := eventToSting(ur.Event)
	return fmt.Sprintf("USR scenario: %s, event: %s, timestamp: %s",
		ur.Scenario,
		event,
		time.UnixMilli(ur.Timestamp),
	)
}

func (ur UserRecord) ToInfluxUserLineParams() (time.Time, string, string) {
	timestamp := toInfluxTimestamp(ur.Timestamp)
	return timestamp, ur.Scenario, eventToSting(ur.Event)
}

type ErrorRecord struct {
	Message   string
	Timestamp int64
}

func (er ErrorRecord) ToInfluxPoint() (*client.Point, error) {
	timestamp := toInfluxTimestamp(er.Timestamp)
	return influx.NewPoint(
		"errors",
		map[string]string{
			"systemUnderTest": systemUnderTest,
			"testEnvironment": testEnvironment,
			"nodeName":        nodeName,
			"simulation":      simulationName,
		},
		map[string]interface{}{
			"errorMessage": er.Message,
		},
		timestamp,
	)
}

type Group struct {
	Hierarchy []string
}

func (g Group) String() string {
	return fmt.Sprintf("%s", g.Hierarchy)
}
