package gatlingparser

import (
	"bufio"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"io"
	"math"
	"strings"

	l "github.com/perfana/x2i/logger"
)

const (
	RunHeaderType byte = iota
	RequestRecordType
	UserRecordType
	GroupRecordType
	ErrorRecordType
)

func ReadInt(reader *bufio.Reader) (int32, error) {
	var int32Value int32
	err := binary.Read(reader, currentByteOrder(), &int32Value)
	return int32Value, err
}

func currentByteOrder() binary.ByteOrder {
	var order binary.ByteOrder = binary.BigEndian
	//if runtime.GOOS == "darwin" && runtime.GOARCH == "arm64" {
	//	order = binary.LittleEndian
	//}
	//l.Debugf("Using byte order: %v for OS: %s, ARCH: %s",
	//	order == binary.LittleEndian,
	//	runtime.GOOS,
	//	runtime.GOARCH,
	//)
	return order
}

func ReadLong(reader *bufio.Reader) (int64, error) {
	var int64Value int64
	err := binary.Read(reader, currentByteOrder(), &int64Value)
	return int64Value, err
}

func sanitize(s string) string {
	return strings.ReplaceAll(strings.ReplaceAll(strings.ReplaceAll(s, "\n", " "), "\r", " "), "\t", " ")
}

func ReadString(reader *bufio.Reader) (string, error) {
	strLength, err := ReadInt(reader)
	if err != nil {
		return "", err
	}

	if strLength == 0 {
		return "", nil
	}

	if strLength < 0 {
		return "", fmt.Errorf("invalid string length: %d", strLength)
	}

	if strLength > 2000 {
		return "", fmt.Errorf("invalid string length: %d", strLength)
	}

	strBytes := make([]byte, strLength)
	_, err = reader.Read(strBytes)
	if err != nil {
		return "", err
	}
	// skip byte of internal Java string serialization format ('coder' field in String class)
	reader.ReadByte()
	readString := string(strBytes)
	fmt.Printf("DEBUG: Read string as %q\n", readString)
	return readString, nil
}

func ReadSanitizedString(reader *bufio.Reader) (string, error) {
	str, err := ReadString(reader)
	if err != nil {
		return "", err
	}
	return sanitize(str), nil
}

var stringCache = make(map[int32]string)

func ReadCachedSanitizedString(reader *bufio.Reader) (string, error) {
	cachedIndex, err := ReadInt(reader)
	if err != nil {
		return "", err
	}

	if cachedIndex >= 0 {
		str, err := ReadString(reader)
		if err != nil {
			return "", err
		}
		sanitizedStr := sanitize(str)
		stringCache[cachedIndex] = sanitizedStr
		return sanitizedStr, nil
	} else {
		cachedString, exists := stringCache[-cachedIndex]
		if !exists {
			return "", fmt.Errorf("cached string missing for index %d", -cachedIndex)
		}
		return cachedString, nil
	}
}

func ReadBool(reader *bufio.Reader) (bool, error) {
	boolByte, err := reader.ReadByte()
	if err != nil {
		return false, err
	}
	return boolByte != 0, nil // transform byte to bool
}

func ReadByteArray(reader *bufio.Reader) ([]byte, error) {
	bytesLength, err := ReadInt(reader)
	if err != nil {
		return nil, err
	}

	if bytesLength == 0 {
		return []byte{}, nil
	}

	if bytesLength < 0 {
		return nil, fmt.Errorf("invalid bytes length: %d", bytesLength)
	}

	bytes := make([]byte, bytesLength)
	_, err = reader.Read(bytes)
	if err != nil {
		return nil, err
	}

	return bytes, nil
}

func ReadRunMessage(reader *bufio.Reader) (RunMessage, error) {
	var result RunMessage
	var err error

	result.GatlingVersion, err = ReadString(reader)
	if err != nil {
		return result, err
	}

	result.SimulationClassName, err = ReadString(reader)
	if err != nil {
		return result, err
	}

	result.Start, err = ReadLong(reader)
	if err != nil {
		return result, err
	}

	result.RunDescription, err = ReadString(reader)
	if err != nil {
		return result, err
	}
	result.SimulationId = "" // not used

	return result, nil
}

func ReadHeader(reader *bufio.Reader) (RunMessage, []string, [][]byte, error) {
	var message RunMessage

	message, err := ReadRunMessage(reader)
	if err != nil {
		return message, nil, nil, err
	}

	scenariosNumber, err := ReadInt(reader)
	if err != nil {
		return message, nil, nil, err
	}

	scenarios := make([]string, scenariosNumber)

	for i := 0; i < int(scenariosNumber); i++ {
		scenarios[i], err = ReadSanitizedString(reader)
		if err != nil {
			return message, nil, nil, err
		}
	}

	assertionsNumber, err := ReadInt(reader)
	if err != nil {
		return message, nil, nil, err
	}

	assertions := make([][]byte, assertionsNumber)

	for i := 0; i < int(assertionsNumber); i++ {
		assertions[i], err = ReadByteArray(reader)
		if err != nil {
			return message, nil, nil, err
		}
	}

	return message, scenarios, assertions, nil

}

func ReadGroup(reader *bufio.Reader) (*Group, error) {
	const maxHierarchyLength = 2000 // adjust to needs

	hierarchyLength, err := ReadInt(reader)
	if err != nil {
		return nil, fmt.Errorf("failed to read hierarchy length: %w", err)
	}

	// Validate the length before allocating slice
	if hierarchyLength < 0 || hierarchyLength > maxHierarchyLength {
		return nil, fmt.Errorf("invalid hierarchy length: %d (must be between 0 and %d)",
			hierarchyLength, maxHierarchyLength)
	}

	hierarchy := make([]string, hierarchyLength)
	for i := int32(0); i < hierarchyLength; i++ {
		hierarchy[i], err = ReadCachedSanitizedString(reader)
		if err != nil {
			return nil, fmt.Errorf("failed to read hierarchy element %d: %w", i, err)
		}
	}

	return &Group{Hierarchy: hierarchy}, nil
}

func ReadRequestRecord(reader *bufio.Reader, runStartTimestamp int64) (RequestRecord, error) {
	var record RequestRecord

	group, err := ReadGroup(reader)
	if err != nil {
		return record, err
	}
	record.Group = group

	record.Name, err = ReadCachedSanitizedString(reader)
	if err != nil {
		return record, err
	}

	start, err := ReadInt(reader)
	if err != nil {
		return record, err
	}
	record.StartTimestamp = int64(start) + runStartTimestamp

	end, err := ReadInt(reader)
	if err != nil {
		return record, err
	}
	record.EndTimestamp = int64(end) + runStartTimestamp

	record.Status, err = ReadBool(reader)
	if err != nil {
		return record, err
	}

	errorMessage, err := ReadCachedSanitizedString(reader)
	if err != nil {
		return record, err
	}
	if errorMessage != "" {
		record.ErrorMessage = &errorMessage
	}

	record.ResponseTime = int32(record.EndTimestamp - record.StartTimestamp)

	if record.EndTimestamp != math.MinInt64 {
		record.Incoming = false
	} else {
		record.Incoming = true
	}

	return record, nil
}

func ReadGroupRecord(reader *bufio.Reader, runStartTimestamp int64) (GroupRecord, error) {
	var record GroupRecord

	group, err := ReadGroup(reader)
	if err != nil {
		return record, err
	}
	record.Group = *group

	start, err := ReadInt(reader)
	if err != nil {
		return record, err
	}
	record.StartTimestamp = int64(start) + runStartTimestamp

	end, err := ReadInt(reader)
	if err != nil {
		return record, err
	}
	record.EndTimestamp = int64(end) + runStartTimestamp

	record.CumulatedResponseTime, err = ReadInt(reader)
	if err != nil {
		return record, err
	}

	record.Status, err = ReadBool(reader)
	if err != nil {
		return record, err
	}

	record.Duration = int32(record.EndTimestamp - record.StartTimestamp)

	return record, nil
}

func ReadUserRecord(reader *bufio.Reader, runStartTimestamp int64, scenarios []string) (UserRecord, error) {
	var record UserRecord

	scenarioIndex, err := ReadInt(reader)
	if err != nil {
		return record, err
	}

	// Get scenario by index
	if scenarioIndex < 0 || scenarioIndex >= int32(len(scenarios)) {
		return record, fmt.Errorf("invalid scenario index: %d", scenarioIndex)
	}
	record.Scenario = scenarios[scenarioIndex]

	// read Start or Stop UserEvent
	record.Event, err = ReadBool(reader)
	if err != nil {
		return record, err
	}

	timestamp, err := ReadInt(reader)
	if err != nil {
		return record, err
	}
	record.Timestamp = int64(timestamp) + runStartTimestamp

	return record, nil
}

func ReadErrorRecord(reader *bufio.Reader, runStartTimestamp int64) (ErrorRecord, error) {
	var record ErrorRecord

	message, err := ReadCachedSanitizedString(reader)
	if err != nil {
		return record, err
	}

	timestamp, err := ReadInt(reader)
	if err != nil {
		return record, err
	}
	record.Timestamp = int64(timestamp) + runStartTimestamp
	record.Message = message

	return record, nil
}

func ReadNotHeaderRecord(reader *bufio.Reader, runStartTimestapm int64, scenarios []string) (interface{}, error) {
	headBytes, err := reader.Peek(1024)
	if err == io.EOF {
		return nil, err
	}
	if err != nil {
		return nil, fmt.Errorf("could not read bytes from log file - %v", err)
	}
	var recordType byte
	recordType, err = reader.ReadByte()
	if err != nil {
		return nil, err
	}

	switch recordType {
	case RequestRecordType:
		return ReadRequestRecord(reader, runStartTimestapm)
	case GroupRecordType:
		return ReadGroupRecord(reader, runStartTimestapm)
	case UserRecordType:
		return ReadUserRecord(reader, runStartTimestapm, scenarios)
	case ErrorRecordType:
		return ReadErrorRecord(reader, runStartTimestapm)
	default:
		l.Errorf("Unknown record start fragment: %s\n", hex.EncodeToString(headBytes))
		return nil, fmt.Errorf("unknown record type: %d", recordType)
	}
}
