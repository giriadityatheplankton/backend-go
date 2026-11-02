package migrate_test

import (
	"context"
	"testing"
	"testing/fstest"

	"backend-go/internal/pkg/database/migrate"

	"github.com/stretchr/testify/assert"
)

func TestRunRejectsNilDB(t *testing.T) {
	err := migrate.NewEngine().Run(context.Background(), nil)
	assert.Error(t, err)
}

func TestSourceNameIsKept(t *testing.T) {
	src := migrate.Source{Name: "platform", FS: fstest.MapFS{"001_x.up.sql": {Data: []byte("select 1")}}, Dir: "."}
	assert.Equal(t, "platform", src.Name)
}
