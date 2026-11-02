//go:build integration

package integration_test

import "database/sql"

var sqlTxSerializable = sql.TxOptions{Isolation: sql.LevelSerializable}
