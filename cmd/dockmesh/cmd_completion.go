package main

import (
	"fmt"
	"os"
)

// runCompletionCmd handles `dockmesh completion <shell>`. Prints a
// completion script to stdout. Users redirect it into the right
// location for their shell.
func runCompletionCmd(args []string) {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: dockmesh completion <bash|zsh|fish>")
		os.Exit(2)
	}
	switch args[0] {
	case "bash":
		fmt.Print(bashCompletion)
	case "zsh":
		fmt.Print(zshCompletion)
	case "fish":
		fmt.Print(fishCompletion)
	default:
		fmt.Fprintf(os.Stderr, "unknown shell: %s (supported: bash, zsh, fish)\n", args[0])
		os.Exit(2)
	}
}

const bashCompletion = `# dockmesh bash completion
# Install: dockmesh completion bash > /etc/bash_completion.d/dockmesh
_dockmesh_complete() {
    local cur prev
    COMPREPLY=()
    cur="${COMP_WORDS[COMP_CWORD]}"
    prev="${COMP_WORDS[COMP_CWORD-1]}"

    local commands="serve admin db ca enroll secrets config doctor completion version help"

    if [ $COMP_CWORD -eq 1 ]; then
        COMPREPLY=( $(compgen -W "$commands" -- "$cur") )
        return 0
    fi

    case "$prev" in
        admin)       COMPREPLY=( $(compgen -W "create reset-password list-users" -- "$cur") ) ;;
        db)          COMPREPLY=( $(compgen -W "migrate backup" -- "$cur") ) ;;
        ca)          COMPREPLY=( $(compgen -W "export rotate" -- "$cur") ) ;;
        enroll)      COMPREPLY=( $(compgen -W "create revoke list" -- "$cur") ) ;;
        secrets)     COMPREPLY=( $(compgen -W "rotate" -- "$cur") ) ;;
        config)      COMPREPLY=( $(compgen -W "show" -- "$cur") ) ;;
        completion)  COMPREPLY=( $(compgen -W "bash zsh fish" -- "$cur") ) ;;
    esac
}
complete -F _dockmesh_complete dockmesh
`

const zshCompletion = `#compdef dockmesh
# dockmesh zsh completion
# Install: dockmesh completion zsh > "${fpath[1]}/_dockmesh"
_dockmesh() {
    local -a commands
    commands=(
        'serve:Start the HTTP + agent mTLS server'
        'admin:Manage users'
        'db:Database operations'
        'ca:Agent PKI operations'
        'enroll:Agent enrollment'
        'secrets:Secrets management'
        'config:Configuration inspection'
        'doctor:Run health checks'
        'completion:Print shell completion script'
        'version:Print version'
        'help:Show help'
    )
    if (( CURRENT == 2 )); then
        _describe 'command' commands
        return
    fi
    case "$words[2]" in
        admin)      _values 'admin subcommand' create reset-password list-users ;;
        db)         _values 'db subcommand' migrate backup ;;
        ca)         _values 'ca subcommand' export rotate ;;
        enroll)     _values 'enroll subcommand' create revoke list ;;
        secrets)    _values 'secrets subcommand' rotate ;;
        config)     _values 'config subcommand' show ;;
        completion) _values 'shell' bash zsh fish ;;
    esac
}
_dockmesh "$@"
`

const fishCompletion = `# dockmesh fish completion
# Install: dockmesh completion fish > ~/.config/fish/completions/dockmesh.fish
complete -c dockmesh -f
complete -c dockmesh -n '__fish_use_subcommand' -a serve  -d 'Start the server'
complete -c dockmesh -n '__fish_use_subcommand' -a admin  -d 'Manage users'
complete -c dockmesh -n '__fish_use_subcommand' -a db     -d 'Database operations'
complete -c dockmesh -n '__fish_use_subcommand' -a ca     -d 'Agent PKI operations'
complete -c dockmesh -n '__fish_use_subcommand' -a enroll -d 'Agent enrollment'
complete -c dockmesh -n '__fish_use_subcommand' -a secrets -d 'Secrets management'
complete -c dockmesh -n '__fish_use_subcommand' -a config  -d 'Show config'
complete -c dockmesh -n '__fish_use_subcommand' -a doctor  -d 'Health checks'
complete -c dockmesh -n '__fish_use_subcommand' -a completion -d 'Shell completion'
complete -c dockmesh -n '__fish_use_subcommand' -a version -d 'Print version'
complete -c dockmesh -n '__fish_use_subcommand' -a help    -d 'Show help'

complete -c dockmesh -n '__fish_seen_subcommand_from admin'  -a 'create reset-password list-users'
complete -c dockmesh -n '__fish_seen_subcommand_from db'     -a 'migrate backup'
complete -c dockmesh -n '__fish_seen_subcommand_from ca'     -a 'export rotate'
complete -c dockmesh -n '__fish_seen_subcommand_from enroll' -a 'create revoke list'
complete -c dockmesh -n '__fish_seen_subcommand_from secrets' -a 'rotate'
complete -c dockmesh -n '__fish_seen_subcommand_from config' -a 'show'
complete -c dockmesh -n '__fish_seen_subcommand_from completion' -a 'bash zsh fish'
`
