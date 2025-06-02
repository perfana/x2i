package gatlingparser

import (
	"bufio"
	"encoding/binary"
	"encoding/hex" // Ensured import
	"fmt"
	"io"
	"math"
	"strings"
	//l "github.com/perfana/x2i/logger" // Keep l for other logs if any, though prompt implies converting all specified ones
)

const (
	RunHeaderType byte = iota
	RequestRecordType
	UserRecordType
	GroupRecordType
	ErrorRecordType
)

func ReadInt(reader *bufio.Reader) (int32, error) {
	var i int32
	const int32ByteSize = 4
	// l.Debugf("ReadInt: Attempting to read %d bytes for int32.", int32ByteSize)
	fmt.Printf("READINT_ATTEMPT_READ_4_BYTES\n")

	err := binary.Read(reader, currentByteOrder(), &i)
	if err != nil {
		// l.Debugf("ReadInt: binary.Read error: %v. Value of i before error (if any part read): %d", err, i)
		fmt.Printf("READINT_BINARY_READ_ERR: %v. Value: %d\n", err, i)
		return 0, err
	}
	// l.Debugf("ReadInt: Successfully read value: %d", i)
	fmt.Printf("READINT_SUCCESS: Value: %d\n", i)
	return i, nil
}

func currentByteOrder() binary.ByteOrder {
	var order binary.ByteOrder = binary.BigEndian
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
	fmt.Printf("READSTRING_CALLED\n")
	strLength, err := ReadInt(reader)
	if err != nil {
		fmt.Printf("READSTRING_ERR: reading length: %v\n", err)
		return "", err
	}

	if strLength == 0 {
		return "", nil
	}

	if strLength < 0 {
		err = fmt.Errorf("invalid string length: %d", strLength)
		fmt.Printf("READSTRING_ERR: %v\n", err)
		return "", err
	}

	if strLength > 2000 { // Assuming this is a reasonable max
		err = fmt.Errorf("string length too large: %d", strLength)
		fmt.Printf("READSTRING_ERR: %v\n", err)
		return "", err
	}

	strBytes := make([]byte, strLength)
	_, err = reader.Read(strBytes)
	if err != nil {
		fmt.Printf("READSTRING_ERR: reading bytes: %v\n", err)
		return "", err
	}
	// skip byte of internal Java string serialization format ('coder' field in String class)
	_, err = reader.ReadByte()
	if err != nil {
		fmt.Printf("READSTRING_ERR: skipping coder byte: %v\n", err)
		return "", err
	}
	readString := string(strBytes)
	// This existing fmt.Printf is fine, can be kept or removed if too noisy later
	// fmt.Printf("DEBUG: Read string as %q\n", readString)
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
	fmt.Printf("READCACHEDSANITIZEDSTRING_CALLED\n")
	cachedIndex, err := ReadInt(reader)
	if err != nil {
		fmt.Printf("READCACHEDSANITIZEDSTRING_ERR: reading index: %v\n", err)
		return "", err
	}

	if cachedIndex >= 0 {
		str, err := ReadString(reader)
		if err != nil {
			fmt.Printf("READCACHEDSANITIZEDSTRING_ERR: reading string for cache: %v\n", err)
			return "", err
		}
		sanitizedStr := sanitize(str)
		stringCache[cachedIndex] = sanitizedStr
		return sanitizedStr, nil
	} else {
		cachedString, exists := stringCache[-cachedIndex]
		if !exists {
			err = fmt.Errorf("cached string missing for index %d", -cachedIndex)
			fmt.Printf("READCACHEDSANITIZEDSTRING_ERR: %v\n", err)
			return "", err
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
	fmt.Printf("READRUNMESSAGE_CALLED\n")
	var result RunMessage
	var err error

	result.GatlingVersion, err = ReadString(reader)
	if err != nil {
		fmt.Printf("READRUNMESSAGE_ERR: GatlingVersion: %v\n", err)
		return result, err
	}

	result.SimulationClassName, err = ReadString(reader)
	if err != nil {
		fmt.Printf("READRUNMESSAGE_ERR: SimulationClassName: %v\n", err)
		return result, err
	}

	result.Start, err = ReadLong(reader)
	if err != nil {
		fmt.Printf("READRUNMESSAGE_ERR: Start: %v\n", err)
		return result, err
	}

	result.RunDescription, err = ReadString(reader)
	if err != nil {
		fmt.Printf("READRUNMESSAGE_ERR: RunDescription: %v\n", err)
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
	fmt.Printf("READGROUP_CALLED\n")
	const maxHierarchyLength = 2000

	hierarchyLength, err := ReadInt(reader)
	if err != nil {
		fmt.Printf("READGROUP_ERR: reading length: %v\n", err)
		return nil, fmt.Errorf("failed to read hierarchy length: %w", err)
	}

	if hierarchyLength < 0 || hierarchyLength > maxHierarchyLength {
		err = fmt.Errorf("invalid hierarchy length: %d (must be between 0 and %d)", hierarchyLength, maxHierarchyLength)
		fmt.Printf("READGROUP_ERR: %v\n", err)
		return nil, err
	}

	hierarchy := make([]string, hierarchyLength)
	for i := int32(0); i < hierarchyLength; i++ {
		hierarchy[i], err = ReadCachedSanitizedString(reader)
		if err != nil {
			fmt.Printf("READGROUP_ERR: reading element %d: %v\n", i, err)
			return nil, fmt.Errorf("failed to read hierarchy element %d: %w", i, err)
		}
	}

	return &Group{Hierarchy: hierarchy}, nil
}

func ReadRequestRecord(reader *bufio.Reader, runStartTimestamp int64) (RequestRecord, error) {
	fmt.Printf("READREQUESTRECORD_CALLED\n")
	var record RequestRecord

	group, err := ReadGroup(reader)
	if err != nil {
		// fmt.Printf("READREQUESTRECORD_ERR: ReadGroup: %v\n", err) // Covered by ReadGroup's own error logging
		return record, err
	}
	record.Group = group
	// fmt.Printf("ReadRequestRecord: Successfully decoded groupHierarchy with %d groups. Continuing to decode other fields.\n", len(group.Hierarchy)) // Changed from l.Debugf

	record.Name, err = ReadCachedSanitizedString(reader)
	if err != nil {
		fmt.Printf("READREQUESTRECORD_ERR: Name: %v\n", err)
		return record, err
	}

	start, err := ReadInt(reader)
	if err != nil {
		fmt.Printf("READREQUESTRECORD_ERR: StartTimestamp: %v\n", err)
		return record, err
	}
	record.StartTimestamp = int64(start) + runStartTimestamp

	end, err := ReadInt(reader)
	if err != nil {
		fmt.Printf("READREQUESTRECORD_ERR: EndTimestamp: %v\n", err)
		return record, err
	}
	record.EndTimestamp = int64(end) + runStartTimestamp

	record.Status, err = ReadBool(reader)
	if err != nil {
		fmt.Printf("READREQUESTRECORD_ERR: Status: %v\n", err)
		return record, err
	}

	errorMessage, err := ReadCachedSanitizedString(reader)
	if err != nil {
		fmt.Printf("READREQUESTRECORD_ERR: ErrorMessage: %v\n", err)
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
	fmt.Printf("READGROUPRECORD_CALLED\n")
	var record GroupRecord

	group, err := ReadGroup(reader)
	if err != nil {
		// fmt.Printf("READGROUPRECORD_ERR: ReadGroup: %v\n", err)
		return record, err
	}
	record.Group = *group

	start, err := ReadInt(reader)
	if err != nil {
		fmt.Printf("READGROUPRECORD_ERR: StartTimestamp: %v\n", err)
		return record, err
	}
	record.StartTimestamp = int64(start) + runStartTimestamp

	end, err := ReadInt(reader)
	if err != nil {
		fmt.Printf("READGROUPRECORD_ERR: EndTimestamp: %v\n", err)
		return record, err
	}
	record.EndTimestamp = int64(end) + runStartTimestamp

	record.CumulatedResponseTime, err = ReadInt(reader)
	if err != nil {
		fmt.Printf("READGROUPRECORD_ERR: CumulatedResponseTime: %v\n", err)
		return record, err
	}

	record.Status, err = ReadBool(reader)
	if err != nil {
		fmt.Printf("READGROUPRECORD_ERR: Status: %v\n", err)
		return record, err
	}

	record.Duration = int32(record.EndTimestamp - record.StartTimestamp)

	return record, nil
}

func ReadUserRecord(reader *bufio.Reader, runStartTimestamp int64, scenarios []string) (UserRecord, error) {
	fmt.Printf("READUSERRECORD_CALLED\n")
	var record UserRecord

	scenarioIndex, err := ReadInt(reader)
	if err != nil {
		fmt.Printf("READUSERRECORD_ERR: ScenarioIndex: %v\n", err)
		return record, err
	}

	if scenarioIndex < 0 || scenarioIndex >= int32(len(scenarios)) {
		err = fmt.Errorf("invalid scenario index: %d", scenarioIndex)
		fmt.Printf("READUSERRECORD_ERR: %v\n", err)
		return record, err
	}
	record.Scenario = scenarios[scenarioIndex]

	record.Event, err = ReadBool(reader)
	if err != nil {
		fmt.Printf("READUSERRECORD_ERR: Event: %v\n", err)
		return record, err
	}

	timestamp, err := ReadInt(reader)
	if err != nil {
		fmt.Printf("READUSERRECORD_ERR: Timestamp: %v\n", err)
		return record, err
	}
	record.Timestamp = int64(timestamp) + runStartTimestamp

	return record, nil
}

func ReadErrorRecord(reader *bufio.Reader, runStartTimestamp int64) (ErrorRecord, error) {
	fmt.Printf("READERRORRECORD_CALLED\n")
	var record ErrorRecord

	message, err := ReadCachedSanitizedString(reader)
	if err != nil {
		fmt.Printf("READERRORRECORD_ERR: Message: %v\n", err)
		return record, err
	}

	timestamp, err := ReadInt(reader)
	if err != nil {
		fmt.Printf("READERRORRECORD_ERR: Timestamp: %v\n", err)
		return record, err
	}
	record.Timestamp = int64(timestamp) + runStartTimestamp
	record.Message = message

	return record, nil
}

func ReadNotHeaderRecord(reader *bufio.Reader, runStartTimestapm int64, scenarios []string) (interface{}, error) {
	fmt.Printf("RNHR_CALLED\n")
	headBytes, errPeek := reader.Peek(8)
	if errPeek != nil && errPeek != io.EOF {
		fmt.Printf("RNHR_PEEK_ERR: %v\n", errPeek)
	} else if errPeek == nil {
		fmt.Printf("RNHR_PEEKED_BYTES: %s\n", hex.EncodeToString(headBytes))
	}

	recordTypeByte, err := reader.ReadByte()
	if err != nil {
		fmt.Printf("RNHR_READBYTE_ERR: %v\n", err)
		return nil, err
	}
	fmt.Printf("RNHR_READ_TYPE_BYTE: %d\n", recordTypeByte)

	switch recordTypeByte {
	case RequestRecordType:
		fmt.Printf("RNHR_DECODING_REQUEST\n")
		return ReadRequestRecord(reader, runStartTimestapm)
	case GroupRecordType:
		fmt.Printf("RNHR_DECODING_GROUP\n")
		return ReadGroupRecord(reader, runStartTimestapm)
	case UserRecordType:
		fmt.Printf("RNHR_DECODING_USER\n")
		return ReadUserRecord(reader, runStartTimestapm, scenarios)
	case ErrorRecordType:
		fmt.Printf("RNHR_DECODING_ERROR\n")
		return ReadErrorRecord(reader, runStartTimestapm)
	default:
		var contextBytesForError []byte
		var hexContext string = "N/A"
		if errPeek == nil && headBytes != nil {
			contextBytesForError = headBytes
		} else {
			peekAgainBytes, peekErr := reader.Peek(16)
			if peekErr == nil {
				contextBytesForError = peekAgainBytes
			}
		}
		if len(contextBytesForError) > 0 {
			hexContext = hex.EncodeToString(contextBytesForError)
		}
		errUnknown := fmt.Errorf("unknown record type: %d", recordTypeByte)
		fmt.Printf("RNHR_UNKNOWN_TYPE_ERR: %v. Context: %s\n", errUnknown, hexContext)
		return nil, errUnknown
	}
}
