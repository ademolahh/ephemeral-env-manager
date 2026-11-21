package db

import "slices"

type Role string

const (
	RoleUser  Role = "user"
	RoleAgent Role = "agent"
)

type EnvType string

const (
	EnvRegularTesting EnvType = "REGULAR_TESTING"
	EnvPRReview       EnvType = "PR_REVIEW"
	EnvProdBug        EnvType = "PROD_BUG"
)

type EnvStatus string

const (
	EnvProvisioning EnvStatus = "PROVISIONING"
	EnvRunning      EnvStatus = "RUNNING"
	EnvDeleting     EnvStatus = "DELETING"
	EnvDeleted      EnvStatus = "DELETED"
	EnvFailed       EnvStatus = "FAILED"
)

// Lowest to highest priority.
var EnvTypes = []EnvType{EnvRegularTesting, EnvPRReview, EnvProdBug}

var EnvStatuses = []EnvStatus{EnvProvisioning, EnvRunning, EnvDeleting, EnvDeleted, EnvFailed}

func (t EnvType) Rank() int {
	return slices.Index(EnvTypes, t) + 1
}

func (t EnvType) Valid() bool {
	return t.Rank() > 0
}
