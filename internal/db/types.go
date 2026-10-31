package db

type Role string

const (
	RoleAdmin Role = "admin"
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
