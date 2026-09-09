package main

import (
	"context"
	"fmt"
	"os"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	"astral/internal/query"
)

// toolPrefix names the MCP tools exposed by this server. Default "astral_"
// yields astral_locate etc. Set ASTRAL_MCP_TOOL_PREFIX to rename (e.g. "" for
// bare locate/module/review/callers/affected, or "codex_" etc.) to test
// whether tool naming affects model behavior.
func toolPrefix() string {
	if p := os.Getenv("ASTRAL_MCP_TOOL_PREFIX"); p != "" {
		return p
	}
	return "astral_"
}

var mcpPrefix = toolPrefix()

func tool(name string) string { return mcpPrefix + name }

func main() {
	root, err := os.Getwd()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	app := query.New(root)

	s := server.NewMCPServer(
		"astral",
		"0.1.0",
		server.WithToolCapabilities(true),
	)

	// locate: file:line + scoped note (with state)
	s.AddTool(
		mcp.NewTool(
			tool("locate"),
			mcp.WithDescription("Locate a symbol in the codebase. Returns file:line plus the scoped conventions note (purpose, conventions, invariants, state). Use this BEFORE reading any source file to find where a symbol is defined and how to write code in that module."),
			mcp.WithString("symbol", mcp.Required(), mcp.Description("Symbol name to locate (e.g. a function, type, or method name)")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			args, _ := req.Params.Arguments.(map[string]any)
			symbol, _ := args["symbol"].(string)
			out, err := app.Locate(symbol)
			if err != nil {
				return mcp.NewToolResultText("error: " + err.Error()), nil
			}
			return mcp.NewToolResultText(out), nil
		},
	)

	// module: per-file summaries + module note
	s.AddTool(
		mcp.NewTool(
			tool("module"),
			mcp.WithDescription("Get per-file summaries and the merged conventions note for a module directory. Use this to learn what a module contains and its conventions before writing code there."),
			mcp.WithString("path", mcp.Required(), mcp.Description("Module directory path, relative to the project root (e.g. endpoint or log)")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			args, _ := req.Params.Arguments.(map[string]any)
			path, _ := args["path"].(string)
			out, err := app.Module(path)
			if err != nil {
				return mcp.NewToolResultText("error: " + err.Error()), nil
			}
			return mcp.NewToolResultText(out), nil
		},
	)

	// review: batched note states
	s.AddTool(
		mcp.NewTool(
			tool("review"),
			mcp.WithDescription("List all conventions notes with their lifecycle state (draft, reviewed, stale, conflict) and diffs for drifted notes. Use this to check whether any notes need re-review before making changes."),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			out, err := app.Review()
			if err != nil {
				return mcp.NewToolResultText("error: " + err.Error()), nil
			}
			return mcp.NewToolResultText(out), nil
		},
	)

	// callers: who references a symbol
	s.AddTool(
		mcp.NewTool(
			tool("callers"),
			mcp.WithDescription("List files that reference a symbol across packages. Use this before refactoring or removing a symbol to understand its blast radius."),
			mcp.WithString("symbol", mcp.Required(), mcp.Description("Symbol name to find callers of")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			args, _ := req.Params.Arguments.(map[string]any)
			symbol, _ := args["symbol"].(string)
			out, err := app.Callers(symbol)
			if err != nil {
				return mcp.NewToolResultText("error: " + err.Error()), nil
			}
			return mcp.NewToolResultText(out), nil
		},
	)

	// affected: test impact
	s.AddTool(
		mcp.NewTool(
			tool("affected"),
			mcp.WithDescription("List test files impacted by changes to the given source files (test impact analysis). Use this after editing to know exactly which tests to run instead of the full suite."),
			mcp.WithArray("files", mcp.Required(), mcp.Description("Source file paths that changed, relative to project root (e.g. endpoint/endpoint.go)")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			args, _ := req.Params.Arguments.(map[string]any)
			raw, ok := args["files"].([]any)
			if !ok {
				return mcp.NewToolResultText("error: files must be a list of paths"), nil
			}
			var paths []string
			for _, f := range raw {
				if s, ok := f.(string); ok {
					paths = append(paths, s)
				}
			}
			out, err := app.Affected(paths)
			if err != nil {
				return mcp.NewToolResultText("error: " + err.Error()), nil
			}
			return mcp.NewToolResultText(out), nil
		},
	)

	if err := server.ServeStdio(s); err != nil {
		fmt.Fprintln(os.Stderr, "astral-mcp:", err)
		os.Exit(1)
	}
}
