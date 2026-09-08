package forge

import (
	"fmt"
	"time"
)

type Error struct {
	Status    int
	Code      string
	Retryable bool
	RetryAt   time.Time
}

func (err *Error) Error() string {
	if err == nil {
		return ""
	}
	return err.Code
}

func NewError(status int, code string, retryable bool) error {
	if status < 400 || status > 599 || code == "" {
		return fmt.Errorf("invalid forge error")
	}
	return &Error{Status: status, Code: code, Retryable: retryable}
}
