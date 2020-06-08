# bizfly-backup
BizFly Cloud backup service.

# Building

```shell script
$ go build
$ ./bizfly-backup
```shell script
  BizFly Cloud backup agent is a CLI application to interact with BizFly Cloud Backup Service.

  Usage:
    bizfly-backup [flags]
    bizfly-backup [command]

  Available Commands:
    agent       Run agent.
    backup      Perform backup tasks.
    help        Help about any command
    restore     Restore a backup.
    upgrade     Upgrade bizfly-backup to latest version.

  Flags:
        --config string   config file (default is $HOME/.bizfly-backup.yaml)
        --debug           enable debug (default is false)
    -h, --help            help for bizfly-backup

  Use "bizfly-backup [command] --help" for more information about a command.
  ```

# Agent

## Help

```shell script
./bizfly-backup agent --help
Run agent.

Usage:
  bizfly-backup agent [flags]

Flags:
      --addr string   listening address of server. (default "unix:///var/folders/y4/hs76ltbn7sb66lw_6934kq4m0000gn/T/bizfly-backup.sock")
  -h, --help          help for agent

Global Flags:
      --config string   config file (default is $HOME/.bizfly-backup.yaml)
      --debug           enable debug (default is false)
```
## Running

```shell script
$ ./bizfly-backup agent --debug=true --config=./conf/agent.yaml
2020-06-08T09:14:26.552+0700	INFO	cmd/root.go:96	Using config file: ./agent.yaml
2020-06-08T09:14:26.559+0700	DEBUG	cmd/agent.go:50	Listening address: unix:///var/folders/y4/hs76ltbn7sb66lw_6934kq4m0000gn/T/bizfly-backup.sock
```
