package db

import "errors"

var (
	ErrWhereClauseRequired = errors.New("kiya: update/delete requires a WHERE clause (safety check)")

	ErrEmptyData = errors.New("kiya: insert/update data cannot be empty")

	ErrTableRequired = errors.New("kiya: table name is required")

	ErrInvalidTableName = errors.New("kiya: invalid table name")

	ErrDestinationNil = errors.New("kiya: destination is nil")

	ErrModelNil = errors.New("kiya: model is nil")

	ErrRecordNotFound = errors.New("kiya: record not found")
)
