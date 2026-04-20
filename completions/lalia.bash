# bash completion for lalia — managed by the lalia repo; reinstalled by
# `make install`.

_lalia() {
    local cur prev
    COMPREPLY=()
    cur="${COMP_WORDS[COMP_CWORD]}"
    prev="${COMP_WORDS[COMP_CWORD-1]}"

    local commands="register unregister init prompt run agents nickname rooms room join leave participants post tell ask read peek read-any channels history task renew stop protocol help --version --help -h"

    if [[ $COMP_CWORD -eq 1 ]]; then
        COMPREPLY=( $(compgen -W "$commands" -- "$cur") )
        return
    fi

    local cmd="${COMP_WORDS[1]}"
    local sub="${COMP_WORDS[2]:-}"

    case "$cmd" in
        init|prompt)
            if [[ $COMP_CWORD -eq 2 ]]; then
                COMPREPLY=( $(compgen -W "peer worker supervisor" -- "$cur") )
            fi
            ;;
        run)
            if [[ $COMP_CWORD -eq 2 ]]; then
                COMPREPLY=( $(compgen -W "peer worker supervisor" -- "$cur") )
            else
                COMPREPLY=( $(compgen -W "--claude-code --codex --copilot --force" -- "$cur") )
            fi
            ;;
        rooms)
            if [[ $COMP_CWORD -eq 2 ]]; then
                COMPREPLY=( $(compgen -W "gc" -- "$cur") )
            fi
            ;;
        room)
            if [[ $COMP_CWORD -eq 2 ]]; then
                COMPREPLY=( $(compgen -W "create" -- "$cur") )
            elif [[ "$sub" == "create" && "$prev" == "--desc" ]]; then
                COMPREPLY=()
            elif [[ "$sub" == "create" ]]; then
                COMPREPLY=( $(compgen -W "--desc" -- "$cur") )
            fi
            ;;
        task)
            if [[ $COMP_CWORD -eq 2 ]]; then
                COMPREPLY=( $(compgen -W "publish bulletin claim status unassign reassign unpublish show list handoff" -- "$cur") )
                return
            fi
            case "$sub" in
                status)
                    if [[ $COMP_CWORD -eq 4 ]]; then
                        COMPREPLY=( $(compgen -W "open assigned in-progress ready blocked merged" -- "$cur") )
                    else
                        COMPREPLY=( $(compgen -W "--project" -- "$cur") )
                    fi
                    ;;
                publish)
                    COMPREPLY=( $(compgen -W "--file --project" -- "$cur") )
                    ;;
                reassign)
                    COMPREPLY=( $(compgen -W "--project" -- "$cur") )
                    ;;
                unpublish)
                    COMPREPLY=( $(compgen -W "--force --wipe-worktree --evict-owner --project" -- "$cur") )
                    ;;
                claim|bulletin|show|list|unassign|handoff)
                    COMPREPLY=( $(compgen -W "--project" -- "$cur") )
                    ;;
            esac
            ;;
        register)
            if [[ "$prev" == "--role" ]]; then
                COMPREPLY=( $(compgen -W "peer supervisor worker" -- "$cur") )
            else
                COMPREPLY=( $(compgen -W "--name --as --role --harness --model --project" -- "$cur") )
            fi
            ;;
        nickname)
            COMPREPLY=( $(compgen -W "--follow -d" -- "$cur") )
            ;;
        join|leave|participants)
            if [[ $COMP_CWORD -eq 2 ]]; then
                local rooms=$(lalia rooms 2>/dev/null | awk '{print $1}')
                COMPREPLY=( $(compgen -W "$rooms" -- "$cur") )
            fi
            ;;
        post)
            if [[ $COMP_CWORD -eq 2 ]]; then
                local rooms=$(lalia rooms 2>/dev/null | awk '{print $1}')
                COMPREPLY=( $(compgen -W "$rooms" -- "$cur") )
            else
                COMPREPLY=( $(compgen -W "--as" -- "$cur") )
            fi
            ;;
        tell|ask)
            if [[ $COMP_CWORD -eq 2 ]]; then
                local peers=$(lalia agents 2>/dev/null | awk '{print $1}')
                COMPREPLY=( $(compgen -W "$peers" -- "$cur") )
            else
                COMPREPLY=( $(compgen -W "--timeout --as" -- "$cur") )
            fi
            ;;
        read|peek|history)
            COMPREPLY=( $(compgen -W "--room --timeout --since --limit --as" -- "$cur") )
            ;;
        read-any)
            COMPREPLY=( $(compgen -W "--timeout --as" -- "$cur") )
            ;;
    esac
}

complete -F _lalia lalia
