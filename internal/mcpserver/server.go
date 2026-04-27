package mcpserver

import (
	"github.com/modelcontextprotocol/go-sdk/mcp"

	pb "github.com/sbogutyn/el-pulpo-ai/internal/proto"
)

// NewServer builds an MCP server wired to the given AdminService client and
// registers every tool the mastermind-mcp binary exposes.
func NewServer(admin pb.AdminServiceClient) *mcp.Server {
	s := mcp.NewServer(&mcp.Implementation{Name: "mastermind-mcp", Version: "v1.0.0"}, nil)
	registerCreateTask(s, admin)
	registerGetTask(s, admin)
	registerListTasks(s, admin)
	registerRequestReview(s, admin)
	registerFinalizeTask(s, admin)
	return s
}
