// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package hook

import (
	"os"
	"regexp"
	"strings"

	"mvdan.cc/sh/v3/syntax"
)

// safeAssignName matches a variable name a line may set as a fresh local.
var safeAssignName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// unsafeEnvValue matches an env-prefix value that is not one plain literal word.
var unsafeEnvValue = regexp.MustCompile("[\\s;|&<>(){}$`'\"\\\\]")

// zshSpecialNames are the lowercase parameters zsh itself reads or that rewrite
// shell state when assigned (`path`, `functions[x]=`, `commands[x]=`, `mapfile[f]=`).
var zshSpecialNames = map[string]bool{
	"_": true, "aliases": true, "argv": true, "argv0": true, "builtins": true,
	"cdpath": true, "commands": true, "dirstack": true, "fignore": true, "fpath": true,
	"funcstack": true, "functions": true, "functions_source": true, "galiases": true,
	"histchars": true, "history": true, "historywords": true, "jobdirs": true,
	"jobstates": true, "jobtexts": true, "keymaps": true, "mailpath": true,
	"manpath": true, "module_path": true, "modules": true, "nameddirs": true,
	"options": true, "parameters": true, "patchars": true, "path": true,
	"pipestatus": true, "prompt": true, "psvar": true, "reswords": true,
	"saliases": true, "signals": true, "status": true, "termcap": true,
	"terminfo": true, "userdirs": true, "usergroups": true, "watch": true,
	"widgets": true, "epochtime": true, "errnos": true, "langinfo": true,
	"mapfile": true, "sysparams": true,
}

// shellVarNames are the parameters bash and zsh read or own, listed exactly: a
// `PS` or `HIST` prefix would refuse ordinary locals such as PSQL_x and HISTORY_x.
var shellVarNames = map[string]bool{
	"IFS": true, "PATH": true, "CDPATH": true, "GLOBIGNORE": true, "GLOBSUBST": true,
	"NULLCMD": true, "READNULLCMD": true, "TMOUT": true, "RANDOM": true, "SRANDOM": true,
	"SECONDS": true, "LINENO": true, "EPOCHSECONDS": true, "EPOCHREALTIME": true, "FUNCNAME": true,
	"FUNCNEST": true, "PIPESTATUS": true, "SHELLOPTS": true, "BASHOPTS": true, "BASH_SOURCE": true,
	"BASH_ARGV": true, "BASH_ARGC": true, "BASH_LINENO": true, "BASH_COMMAND": true, "BASH_SUBSHELL": true,
	"BASH_ENV": true, "BASH_XTRACEFD": true, "ENV": true, "ZDOTDIR": true, "FPATH": true,
	"MANPATH": true, "MAIL": true, "MAILPATH": true, "MAILCHECK": true, "WATCH": true,
	"WORDCHARS": true, "LISTMAX": true, "REPORTTIME": true, "KEYTIMEOUT": true, "MATCH": true,
	"MBEGIN": true, "MEND": true, "REPLY": true, "OPTARG": true, "OPTIND": true,
	"OPTERR": true, "FIGNORE": true, "TIMEFMT": true, "TMPPREFIX": true, "COLUMNS": true,
	"LINES": true, "PS1": true, "PS2": true, "PS3": true, "PS4": true,
	"RPROMPT": true, "HISTFILE": true, "HISTSIZE": true, "HISTFILESIZE": true, "SAVEHIST": true,
	"HISTCHARS": true, "HISTIGNORE": true, "HISTNOSTORE": true, "HISTREDUCEBLANKS": true, "HISTVERIFY": true,
	"HISTFCNTLLOCK": true, "HISTCONTROL": true, "HISTTIMEFORMAT": true, "HISTCMD": true, "TRANSIENT_RPROMPT": true,
	"BASHPID": true, "EUID": true, "UID": true, "PPID": true, "PWD": true,
	"OLDPWD": true, "HOSTNAME": true, "SHLVL": true, "TIMEFORMAT": true,
}

// envRedirectNames change where a tool reads from or writes to, so `NAME=<v> cmd`
// is not a fresh local: `KUBECONFIG=<other> kubectl …` reads another cluster.
var envRedirectNames = map[string]bool{
	"KUBECONFIG": true, "KUBECACHEDIR": true, "GIT_SSH": true, "GIT_SSH_COMMAND": true,
	"GIT_EXEC_PATH": true, "NODE_OPTIONS": true, "PYTHONPATH": true,
	"PYTHONSTARTUP": true, "PYTHONHOME": true, "PERL5LIB": true, "PERL5OPT": true,
	"RUBYOPT": true, "RUBYLIB": true, "JAVA_TOOL_OPTIONS": true, "_JAVA_OPTIONS": true,
	"TEAMVAULT_CONFIG": true, "SSH_AUTH_SOCK": true, "SSL_CERT_FILE": true,
	"SSL_CERT_DIR": true, "CURL_CA_BUNDLE": true, "REQUESTS_CA_BUNDLE": true,
	"TMPDIR": true, "SHELL": true, "PWD": true, "OLDPWD": true, "ZDOTDIR": true,
}

// localName reports whether name may be assigned as a fresh, unexported local: not
// a name the shell, zsh or the environment already carries.
func localName(name string) bool {
	if !safeAssignName.MatchString(name) || shellReadName(name) || zshSpecialNames[name] ||
		shellVarNames[name] {
		return false
	}
	for _, prefix := range []string{"zsh_", "zle_", "zcurses_", "dis_", "COMP_", "ZSH_",
		"ZLE_", "PROMPT", "READLINE_", "BASH_"} {
		if strings.HasPrefix(name, prefix) {
			return false
		}
	}
	if strings.HasSuffix(name, "_functions") {
		return false
	}
	_, inEnv := os.LookupEnv(name)
	return !inEnv
}

// assigns checks the assignments of a call. A bare assignment (no command) sets a
// fresh local; an env prefix (`NAME=<literal> cmd`) must also not redirect a tool
// and its value must be one plain literal word.
func (w *compoundWalker) assigns(assigns []*syntax.Assign, prefix bool) error {
	for _, assign := range assigns {
		if assign.Name == nil || assign.Index != nil || assign.Array != nil || assign.Naked {
			return errCompoundUnmodelled
		}
		name := assign.Name.Value
		if !localName(name) {
			return errCompoundUnmodelled
		}
		if !prefix {
			if assign.Value != nil {
				if err := w.dynamicWord(assign.Value); err != nil {
					return err
				}
			}
			continue
		}
		if envRedirectNames[name] {
			return errCompoundUnmodelled
		}
		value, ok := literalWord(assign.Value)
		if !ok || value == "" || unsafeEnvValue.MatchString(value) {
			return errCompoundUnmodelled
		}
	}
	return nil
}

// dynamicWord checks a word that may carry a parameter expansion or a command
// substitution. The substitution's commands are walked like any other, so each must
// be allowed on its own; an expansion may only read, never assign or re-evaluate.
func (w *compoundWalker) dynamicWord(word *syntax.Word) error {
	return w.wordParts(word.Parts, false)
}

// wordParts checks the parts of a word or of a double-quoted span. Inside double
// quotes a backslash only escapes `$`, a backtick, `"`, `\` or a newline, so
// `"a\|b"` stays the literal text a rule is matched against; unquoted, `\-o`
// becomes `-o` and would hide a flag from a gated rule.
func (w *compoundWalker) wordParts(parts []syntax.WordPart, quoted bool) error {
	for _, part := range parts {
		switch node := part.(type) {
		case *syntax.Lit:
			if !quoted && strings.Contains(node.Value, "\\") {
				return errCompoundUnmodelled
			}
		case *syntax.SglQuoted:
			if node.Dollar {
				return errCompoundUnmodelled
			}
		case *syntax.DblQuoted:
			if err := w.wordParts(node.Parts, true); err != nil {
				return err
			}
		case *syntax.ParamExp:
			if !safeParamExp(node) {
				return errCompoundUnmodelled
			}
		case *syntax.CmdSubst:
			if err := w.stmts(node.Stmts); err != nil {
				return err
			}
		default:
			return errCompoundUnmodelled
		}
	}
	return nil
}

// safeParamExp reports whether an expansion only reads a variable. `${x:=w}`
// assigns, `${x@P}` runs prompt expansion, `${!x}` reads a name chosen at run time,
// and an index or slice carries arithmetic; each is refused, as is an operand word
// that is not a plain literal.
func safeParamExp(exp *syntax.ParamExp) bool {
	if exp.Index != nil && !literalIndex(exp.Index) {
		return false
	}
	if exp.Excl || exp.Slice != nil || exp.Repl != nil ||
		exp.Names != 0 || exp.Width {
		return false
	}
	if exp.Exp == nil {
		return true
	}
	switch exp.Exp.Op {
	case syntax.DefaultUnset, syntax.DefaultUnsetOrNull, syntax.AlternateUnset,
		syntax.AlternateUnsetOrNull, syntax.ErrorUnset, syntax.ErrorUnsetOrNull,
		syntax.RemSmallPrefix, syntax.RemLargePrefix, syntax.RemSmallSuffix,
		syntax.RemLargeSuffix, syntax.UpperFirst, syntax.UpperAll, syntax.LowerFirst,
		syntax.LowerAll:
	default:
		return false
	}
	return exp.Exp.Word == nil || literalParts(exp.Exp.Word)
}

// sourceText returns the command's own text for the call's arguments, the form an
// allow rule is written against: `echo "=== $c ==="` matches `Bash(echo:*)`.
func (w *compoundWalker) sourceText(args []*syntax.Word) string {
	texts := make([]string, 0, len(args))
	for _, arg := range args {
		start, end := int(arg.Pos().Offset()), int(arg.End().Offset())
		if start < 0 || end > len(w.src) || start > end {
			return ""
		}
		texts = append(texts, w.src[start:end])
	}
	return strings.Join(texts, " ")
}

// literalIndex reports whether an array index is a plain number (`${PIPESTATUS[0]}`);
// any other index is arithmetic that can carry a substitution.
func literalIndex(index syntax.ArithmExpr) bool {
	word, ok := index.(*syntax.Word)
	if !ok {
		return false
	}
	value, ok := literalWord(word)
	return ok && allDigits(value)
}
