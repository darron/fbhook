package memory_test

import (
	"github.com/darron/fbhook/storage/contract"
	"github.com/darron/fbhook/storage/memory"
	"testing"
)

func TestContract(t *testing.T) { contract.Run(t, memory.New()) }
