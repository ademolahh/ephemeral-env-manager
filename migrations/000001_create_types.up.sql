CREATE TYPE caller_role AS ENUM ('user', 'agent');

CREATE TYPE env_type AS ENUM ('REGULAR_TESTING', 'PR_REVIEW', 'PROD_BUG');

CREATE TYPE env_status AS ENUM ('PROVISIONING', 'RUNNING', 'DELETING', 'DELETED', 'FAILED');

CREATE TYPE provider AS ENUM ('docker', 'cloud');
