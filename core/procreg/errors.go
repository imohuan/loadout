package procreg

import "errors"

var (
	errNotFound = errors.New("procreg: process not found")
	errEmptyName = errors.New("procreg: Name is required")
)
