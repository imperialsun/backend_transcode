package api

import (
	"fmt"
	"strings"
)

type demeterQueuePurgeScope string

const (
	demeterQueuePurgeScopeCompleted demeterQueuePurgeScope = "completed"
	demeterQueuePurgeScopeAll       demeterQueuePurgeScope = "all"
)

func parseDemeterQueuePurgeScope(raw string) (demeterQueuePurgeScope, error) {
	scope := demeterQueuePurgeScope(strings.ToLower(strings.TrimSpace(raw)))
	if scope == "" {
		return demeterQueuePurgeScopeCompleted, nil
	}
	switch scope {
	case demeterQueuePurgeScopeCompleted, demeterQueuePurgeScopeAll:
		return scope, nil
	default:
		return "", fmt.Errorf("invalid scope")
	}
}
