package gitforge

import (
	"encoding/hex"
	"fmt"
	"strings"
)

// ReceivePackCommand is one Git receive-pack ref update.
type ReceivePackCommand struct {
	OldOID string
	NewOID string
	Ref    string
}

const zeroOID = "0000000000000000000000000000000000000000"

// ParseReceivePackCommands reads pkt-line commands that precede the pack.
func ParseReceivePackCommands(body []byte) ([]ReceivePackCommand, error) {
	if len(body) < 4 {
		return nil, fmt.Errorf("receive-pack body is too short")
	}
	commands := make([]ReceivePackCommand, 0, 4)
	offset := 0
	for offset+4 <= len(body) {
		lengthBytes := body[offset : offset+4]
		if string(lengthBytes) == "0000" || string(lengthBytes) == "0001" || string(lengthBytes) == "0002" {
			break
		}
		length, err := hex.DecodeString(string(lengthBytes))
		if err != nil || len(length) != 2 {
			return nil, fmt.Errorf("invalid receive-pack pkt-line length")
		}
		size := int(length[0])<<8 | int(length[1])
		if size < 4 || offset+size > len(body) {
			return nil, fmt.Errorf("invalid receive-pack pkt-line")
		}
		payload := body[offset+4 : offset+size]
		offset += size
		if command, ok := parseReceivePackCommand(payload); ok {
			commands = append(commands, command)
		}
	}
	if len(commands) == 0 {
		return nil, fmt.Errorf("receive-pack command list is empty")
	}
	return commands, nil
}

func parseReceivePackCommand(payload []byte) (ReceivePackCommand, bool) {
	text := string(payload)
	if idx := strings.IndexByte(text, 0); idx >= 0 {
		text = text[:idx]
	}
	text = strings.TrimRight(text, "\n\r")
	fields := strings.Fields(text)
	if len(fields) < 3 {
		return ReceivePackCommand{}, false
	}
	oldOID, newOID, ref := strings.ToLower(fields[0]), strings.ToLower(fields[1]), fields[2]
	if !isFullObjectID(oldOID) || !isFullObjectID(newOID) || ref == "" {
		return ReceivePackCommand{}, false
	}
	return ReceivePackCommand{OldOID: oldOID, NewOID: newOID, Ref: ref}, true
}

func EncodeReceivePackCommands(commands []ReceivePackCommand) []byte {
	var body []byte
	for index, command := range commands {
		line := command.OldOID + " " + command.NewOID + " " + command.Ref
		if index == 0 {
			line += "\x00report-status"
		}
		line += "\n"
		body = append(body, []byte(fmt.Sprintf("%04x", 4+len(line)))...)
		body = append(body, line...)
	}
	return append(body, []byte("0000")...)
}

func IsZeroOID(value string) bool {
	return strings.Trim(strings.ToLower(value), "0") == "" || strings.EqualFold(value, zeroOID)
}
