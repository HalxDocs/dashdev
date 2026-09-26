package process

import (
	"time"

	"github.com/HalxDocs/dashdev/internal/service"
)

// action is an operation the owner goroutine performs on a single service.
type action uint8

const (
	actionStart action = iota
	actionStop
	actionRestart
)

// request is a command for the owner goroutine. The set is closed: every
// implementation lives in this file, so the owner's type switch is exhaustive
// and adding a command is a compile-time concern.
type request interface {
	isRequest()
}

// actionRequest asks the owner to start, stop or restart one service.
type actionRequest struct {
	action action
	name   string
	reply  chan error
}

func (*actionRequest) isRequest() {}

// snapshotRequest asks the owner for the current status of every service.
type snapshotRequest struct {
	reply chan snapshotReply
}

func (*snapshotRequest) isRequest() {}

// snapshotReply carries the answer to a snapshotRequest.
type snapshotReply struct {
	statuses []service.Status
	err      error
}

// noticeKind identifies a report from a worker goroutine.
type noticeKind uint8

const (
	// noticeExit reports that a process has been reaped. It must be delivered:
	// a service whose exit is lost would stay "running" forever.
	noticeExit noticeKind = iota
	// noticeSignalError reports the outcome of a stop request.
	noticeSignalError
	// noticeProbe reports the result of one health check.
	noticeProbe
	// noticeDelay reports that a restart backoff has elapsed.
	noticeDelay
)

// notice is how a worker goroutine reports back to the owner.
//
// Workers never touch service state; they send one of these and the owner
// decides what it means. Every notice carries the generation of the process it
// refers to, so a message from a process that has already been replaced is
// recognised as history rather than applied to its successor.
type notice struct {
	kind       noticeKind
	name       string
	generation uint64
	at         time.Time

	exit  ExitInfo
	err   error
	probe probeOutcome
}

// probeOutcome is the result of one health check.
type probeOutcome struct {
	passed  bool
	message string
	at      time.Time
}
