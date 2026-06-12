package toolname

import "strings"

var normalizedToolNameFallbacks = map[string]string{
	"str_replace_editor": "Edit",
	"edit":               "Edit",
	"apply_file_diffs":   "Edit",

	"view":       "Read",
	"readfile":   "Read",
	"read_file":  "Read",
	"read_files": "Read",
	"read":       "Read",

	"listdir":        "Glob",
	"list_dir":       "Glob",
	"list_directory": "Glob",
	"ls":             "Glob",
	"globtool":       "Glob",
	"glob":           "Glob",
	"find_files":     "Glob",
	"file_glob":      "Glob",
	"file_glob_v2":   "Glob",

	"ripgreptool":     "Grep",
	"ripgrep":         "Grep",
	"search_code":     "Grep",
	"search_codebase": "Grep",
	"grep":            "Grep",

	"exec":              "Bash",
	"execute":           "Bash",
	"execute_command":   "Bash",
	"execute-command":   "Bash",
	"run_command":       "Bash",
	"runcommand":        "Bash",
	"launch-process":    "Bash",
	"run_shell_command": "Bash",
	"shell":             "Bash",
	"bash":              "Bash",

	"writefile":   "Write",
	"write_file":  "Write",
	"create_file": "Write",
	"createfile":  "Write",
	"save-file":   "Write",
	"write":       "Write",

	"update_todo_list": "TodoWrite",
	"todo":             "TodoWrite",
	"todo_write":       "TodoWrite",
	"todowrite":        "TodoWrite",

	"web_fetch":                "web_fetch",
	"webfetch":                 "web_fetch",
	"fetch":                    "web_fetch",
	"builtin_web_fetch":        "web_fetch",
	"mcp__fetch__fetch":        "web_fetch",
	"mcp__tavily__web_extract": "web_fetch",

	"web_search":              "web_search",
	"websearch":               "web_search",
	"builtin_web_search":      "web_search",
	"mcp__tavily__web_search": "web_search",
	"mcp__brave__web_search":  "web_search",

	"ask_followup_question": "AskUserQuestion",
	"ask":                   "AskUserQuestion",

	"enter_plan_mode": "EnterPlanMode",
	"exit_plan_mode":  "ExitPlanMode",

	"new_task":       "Task",
	"agent":          "Task",
	"subagent":       "Task",
	"subagents":      "Task",
	"spawn_agent":    "Task",
	"spawn_subagent": "Task",
	"session_spawn":  "Task",
	"sessions_spawn": "Task",

	"task_output": "TaskOutput",
	"task_stop":   "TaskStop",

	"use_skill": "Skill",
	"skill":     "Skill",
}

func NormalizeToolNameFallback(name string) string {
	if mapped, ok := normalizedToolNameFallbacks[strings.ToLower(strings.TrimSpace(name))]; ok {
		return mapped
	}
	return name
}
