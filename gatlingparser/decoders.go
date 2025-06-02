package gatlingparser

import (
	"bufio"
	"encoding/binary"
	"errors" // Added errors import
	"fmt"    // Ensure fmt is imported
	"io"
	"math"
	"strings"
	// l "github.com/perfana/x2i/logger" // Logger import is removed
)

var ErrPartialRecord = errors.New("record data incomplete, more bytes needed")

const (
	RunHeaderType byte = iota
	RequestRecordType
	UserRecordType
	GroupRecordType
	ErrorRecordType
)

func ReadInt(reader *bufio.Reader) (int32, error) {
	var i int32
	err := binary.Read(reader, currentByteOrder(), &i)
	if err != nil {
		// If EOF or UnexpectedEOF occurs, it means the field was truncated.
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			return 0, ErrPartialRecord // Return custom error
		}
		// For other errors, propagate them as is.
		return 0, err
	}
	return i, nil
}

func currentByteOrder() binary.ByteOrder {
	var order binary.ByteOrder = binary.BigEndian
	return order
}

func ReadLong(reader *bufio.Reader) (int64, error) {
	var int64Value int64
	err := binary.Read(reader, currentByteOrder(), &int64Value)
	// Refactor to return ErrPartialRecord if needed by other functions
	if err != nil && (errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF)) {
		return 0, ErrPartialRecord
	}
	return int64Value, err
}

func sanitize(s string) string {
	return strings.ReplaceAll(strings.ReplaceAll(strings.ReplaceAll(s, "\n", " "), "\r", " "), "\t", " ")
}

func ReadString(reader *bufio.Reader) (string, error) {
	strLength, err := ReadInt(reader) // Uses refactored ReadInt
	if err != nil {
		// If ReadInt returns ErrPartialRecord, propagate it.
		// Otherwise, it's a different error from ReadInt (already logged there) or a non-partial error before it.
		return "", err // Propagate ErrPartialRecord or other errors
	}

	if strLength == 0 {
		return "", nil
	}

	if strLength < 0 {
		err = fmt.Errorf("invalid string length: %d", strLength)
		return "", err
	}

	if strLength > 2000 {
		err = fmt.Errorf("string length too large: %d", strLength)
		return "", err
	}

	strBytes := make([]byte, strLength)
	// Use io.ReadFull to ensure all bytes are read or return ErrUnexpectedEOF (which we'll map)
	_, err = io.ReadFull(reader, strBytes)
	if err != nil {
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			return "", ErrPartialRecord
		}
		return "", err
	}

	// skip byte of internal Java string serialization format ('coder' field in String class)
	_, err = reader.ReadByte()
	if err != nil {
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			return "", ErrPartialRecord
		}
		return "", err
	}
	readString := string(strBytes)
	return readString, nil
}

func ReadSanitizedString(reader *bufio.Reader) (string, error) {
	str, err := ReadString(reader)
	if err != nil {
		return "", err // Propagate ErrPartialRecord or other errors
	}
	return sanitize(str), nil
}

var stringCache = make(map[int32]string)

func ReadCachedSanitizedString(reader *bufio.Reader) (string, error) {
	cachedIndex, err := ReadInt(reader) // Uses refactored ReadInt
	if err != nil {
		return "", err
	}

	if cachedIndex >= 0 {
		str, err := ReadString(reader) // Uses refactored ReadString
		if err != nil {
			return "", err
		}
		sanitizedStr := sanitize(str)
		stringCache[cachedIndex] = sanitizedStr
		return sanitizedStr, nil
	} else {
		cachedString, exists := stringCache[-cachedIndex]
		if !exists {
			err = fmt.Errorf("cached string missing for index %d", -cachedIndex)
			return "", err
		}
		return cachedString, nil
	}
}

func ReadBool(reader *bufio.Reader) (bool, error) {
	boolByte, err := reader.ReadByte()
	if err != nil {
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			return false, ErrPartialRecord
		}
		return false, err
	}
	return boolByte != 0, nil
}

func ReadByteArray(reader *bufio.Reader) ([]byte, error) {
	bytesLength, err := ReadInt(reader) // Uses refactored ReadInt
	if err != nil {
		return nil, err // Propagate ErrPartialRecord or other errors
	}

	if bytesLength == 0 {
		return []byte{}, nil
	}

	if bytesLength < 0 {
		return nil, fmt.Errorf("invalid bytes length: %d", bytesLength)
	}
	if bytesLength > 2000000 { // Safety break for very large byte arrays
		return nil, fmt.Errorf("byte array length too large: %d", bytesLength)
	}

	bytes := make([]byte, bytesLength)
	_, err = io.ReadFull(reader, bytes)
	if err != nil {
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			return nil, ErrPartialRecord
		}
		return nil, err
	}
	return bytes, nil
}

// Decode<Type>Record functions
// Each must now first read and verify its type byte, then decode fields.
// If any helper returns ErrPartialRecord, it should be propagated.

func DecodeRunMessage(reader *bufio.Reader) (RunMessage, error) {
	// Type byte already consumed by ReadHeader or initial version of RNHR for RunMessage
	// For this refactor, assuming ReadHeader handles its own type byte if needed.
	// This function is called by ReadHeader, not ReadNotHeaderRecord.
	// Thus, its internal logic for reading fields should map to ErrPartialRecord.

	var result RunMessage
	var err error

	result.GatlingVersion, err = ReadString(reader)
	if err != nil {
		return result, err
	} // Propagates ErrPartialRecord from ReadString

	result.SimulationClassName, err = ReadString(reader)
	if err != nil {
		return result, err
	}

	result.Start, err = ReadLong(reader) // ReadLong also updated to return ErrPartialRecord
	if err != nil {
		return result, err
	}

	result.RunDescription, err = ReadString(reader)
	if err != nil {
		return result, err
	}

	result.SimulationId = ""
	return result, nil
}

// ReadHeader is not a Decode<Type>Record, it's a higher level construct.
// Its internal calls to ReadRunMessage and ReadInt/ReadString for scenarios/assertions
// will now correctly propagate ErrPartialRecord if they encounter partial data.
func ReadHeader(reader *bufio.Reader) (RunMessage, []string, [][]byte, error) {
	var message RunMessage

	// Assuming the very first byte (RunHeaderType) was already peeked/read by caller like processLogHeader
	// For this specific refactor, we'll let DecodeRunMessage be the one that would be called
	// if ReadNotHeaderRecord was generic enough to handle RunMessage too.
	// However, processLogHeader calls ReadHeader, which calls ReadRunMessage directly.
	// So, DecodeRunMessage is the refactored ReadRunMessage.
	message, err := DecodeRunMessage(reader) // Changed to DecodeRunMessage
	if err != nil {
		return message, nil, nil, err // Propagates ErrPartialRecord
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

func DecodeGroup(reader *bufio.Reader) (*Group, error) { // Renamed from ReadGroup
	const maxHierarchyLength = 2000
	
	hierarchyLength, err := ReadInt(reader)
	if err != nil {
		return nil, err
	}

	if hierarchyLength < 0 || hierarchyLength > maxHierarchyLength {
		err = fmt.Errorf("invalid hierarchy length: %d (must be between 0 and %d)", hierarchyLength, maxHierarchyLength)
		return nil, err
	}

	hierarchy := make([]string, hierarchyLength)
	for i := int32(0); i < hierarchyLength; i++ {
		hierarchy[i], err = ReadCachedSanitizedString(reader)
		if err != nil {
			return nil, err
		} // Propagates ErrPartialRecord
	}
	return &Group{Hierarchy: hierarchy}, nil
}

func DecodeRequestRecord(reader *bufio.Reader, runStartTimestamp int64) (RequestRecord, error) {
	var record RequestRecord

	typeByte, err := reader.ReadByte()
	if err != nil {
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			return RequestRecord{}, ErrPartialRecord
		}
		return RequestRecord{}, err
	}
	if typeByte != RequestRecordType {
		return RequestRecord{}, fmt.Errorf("DECODEREQUESTRECORD_ERR: Mismatched type byte %d", typeByte)
	}

	group, err := DecodeGroup(reader) // Changed from ReadGroup
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
	record.Incoming = record.EndTimestamp == math.MinInt64
	return record, nil
}

func DecodeGroupRecord(reader *bufio.Reader, runStartTimestamp int64) (GroupRecord, error) {
	var record GroupRecord

	typeByte, err := reader.ReadByte()
	if err != nil {
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			return GroupRecord{}, ErrPartialRecord
		}
		return GroupRecord{}, err
	}
	if typeByte != GroupRecordType {
		return GroupRecord{}, fmt.Errorf("DECODEGROUPRECORD_ERR: Mismatched type byte %d", typeByte)
	}

	group, err := DecodeGroup(reader) // Changed from ReadGroup
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

func DecodeUserRecord(reader *bufio.Reader, runStartTimestamp int64, scenarios []string) (UserRecord, error) {
	var record UserRecord

	typeByte, err := reader.ReadByte()
	if err != nil {
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			return UserRecord{}, ErrPartialRecord
		}
		return UserRecord{}, err
	}
	if typeByte != UserRecordType {
		return UserRecord{}, fmt.Errorf("DECODEUSERRECORD_ERR: Mismatched type byte %d", typeByte)
	}

	scenarioIndex, err := ReadInt(reader)
	if err != nil {
		return record, err
	}

	if scenarioIndex < 0 || scenarioIndex >= int32(len(scenarios)) {
		err = fmt.Errorf("invalid scenario index: %d", scenarioIndex)
		return record, err
	}
	record.Scenario = scenarios[scenarioIndex]

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

func DecodeErrorRecord(reader *bufio.Reader, runStartTimestamp int64) (ErrorRecord, error) {
	var record ErrorRecord

	typeByte, err := reader.ReadByte()
	if err != nil {
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			return ErrorRecord{}, ErrPartialRecord
		}
		return ErrorRecord{}, err
	}
	if typeByte != ErrorRecordType {
		return ErrorRecord{}, fmt.Errorf("DECODEERRORRECORD_ERR: Mismatched type byte %d", typeByte)
	}

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

// This is the active ReadNotHeaderRecord function that will be refactored per Step 4
func ReadNotHeaderRecord(reader *bufio.Reader, runStartTimestamp int64, scenarios []string) (interface{}, error) {
	peekedType, err := reader.Peek(1)
	if err != nil {
		if errors.Is(err, io.EOF) { // Clean EOF before even a type byte can be peeked
			return nil, io.EOF
		}
		return nil, err // Other peek error
	}
	recordTypeByte := peekedType[0]
	// No RecordHeader type here, just use the byte

	switch recordTypeByte {
	case UserRecordType:
		return DecodeUserRecord(reader, runStartTimestamp, scenarios)
	case RequestRecordType:
		return DecodeRequestRecord(reader, runStartTimestamp)
	case GroupRecordType:
		return DecodeGroupRecord(reader, runStartTimestamp)
	case ErrorRecordType:
		return DecodeErrorRecord(reader, runStartTimestamp)
	default:
		// Consume the bad byte to help with debugging
		badByte, _ := reader.ReadByte()
		
		// Read more context for debugging
		context, _ := reader.Peek(10)
		var hexContext strings.Builder
		for _, b := range context {
			hexContext.WriteString(fmt.Sprintf("%02x ", b))
		}
		
		errUnknown := fmt.Errorf("unknown record type: %d", badByte)
		return nil, errUnknown
	}
}
