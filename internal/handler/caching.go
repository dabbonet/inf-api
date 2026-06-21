package handler

import (
	"orchids-api/internal/prompt"
	"strings"
)

// ApplyCacheStrategy applies the selected cache strategy to the request messages and system prompt.
// Supported strategies:
// - "split": Caches the system prompt and the second-to-last user turn (standard checkpointing).
// - "mixed": Caches system prompt, tools (if applicable), and attempts to cache large context blocks in the last user message.
func applyCacheStrategy(req *ClaudeRequest, strategy string) {
	strategy = strings.ToLower(strings.TrimSpace(strategy))
	if strategy == "mix" {
		strategy = "mixed"
	}
	if strategy == "" || strategy == "none" || strategy == "off" {
		return
	}

	breakpoints := 0
	const maxBreakpoints = 4

	// Helper to add cache control
	addCache := func(block *prompt.ContentBlock) bool {
		if breakpoints >= maxBreakpoints {
			return false
		}
		if block.CacheControl == nil {
			block.CacheControl = &prompt.CacheControl{Type: "ephemeral"}
			breakpoints++
			return true
		}
		return false
	}

	// 1. System Prompt
	// Always cache the last system block if available
	if len(req.System) > 0 {
		// Cache the last system item
		lastSys := &req.System[len(req.System)-1]
		if lastSys.CacheControl == nil {
			lastSys.CacheControl = &prompt.CacheControl{Type: "ephemeral"}
			breakpoints++
		}
	}

	// 2. Messages
	// Iterate backwards to find suitable breakpoints

	// Strategy "Split": Cache 2nd to last user message (history checkpoint)
	// Strategy "Mixed": Cache 2nd to last user message AND large blocks in last message

	msgLen := len(req.Messages)
	if msgLen == 0 {
		return
	}

	// Find user turns indices
	var userIndices []int
	for i, msg := range req.Messages {
		if msg.Role == "user" {
			userIndices = append(userIndices, i)
		}
	}

	// Turn Checkpoint (2nd to last user message) - Common for both strategies
	// This caches the conversation history up to that point.
	if len(userIndices) >= 2 {
		idx := userIndices[len(userIndices)-2]
		msg := &req.Messages[idx]
		if !msg.Content.IsString() {
			blocks := msg.Content.GetBlocks()
			if len(blocks) > 0 {
				// Cache the last block of this message
				if addCache(&blocks[len(blocks)-1]) {
					req.Messages[idx].Content.Blocks = blocks // Update
				}
			}
		}
	}

	// Last User Message
	// For "Mixed" or "Split" (if we treat Split as just history + static context)
	// If the last message has large content (e.g., file dump), cache it.
	if len(userIndices) > 0 {
		idx := userIndices[len(userIndices)-1]
		msg := &req.Messages[idx]
		if !msg.Content.IsString() {
			blocks := msg.Content.GetBlocks()
			// If Mixed strategy, check all blocks
			// If Split strategy, just check the last one
			if len(blocks) > 0 {
				if strategy == "mixed" {
					var largestBlock *prompt.ContentBlock
					maxLen := 0
					for i := range blocks {
						// Estimate size roughly
						size := len(blocks[i].Text)
						if blocks[i].Type == "image" && blocks[i].Source != nil {
							size = len(blocks[i].Source.Data)
						}
						// Find largest
						if size >= maxLen {
							maxLen = size
							largestBlock = &blocks[i]
						}
					}
					if largestBlock != nil {
						addCache(largestBlock)
						req.Messages[idx].Content.Blocks = blocks
					}
				} else {
					// Split strategy: cache last block
					if addCache(&blocks[len(blocks)-1]) {
						req.Messages[idx].Content.Blocks = blocks
					}
				}
			}
		}
	}

	// Tools?
	// If tools are passed as content blocks (some agents do this), cache them.
	// But req.Tools is separate.
}
