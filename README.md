# bizfly-backup
BizFly Cloud backup service.

# Building

```shell script
$ go build
$ ./bizfly-backup
BizFly Cloud backup agent is a CLI application to interact with BizFly Cloud Backup Service.

Usage:
  bizfly-backup [flags]
  bizfly-backup [command]

Available Commands:
  agent         Run agent.
  backup        Perform backup tasks.
  cleanup-cache Remove old cache directories.
  help          Help about any command
  restore       Restore a backup.
  upgrade       Upgrade bizfly-backup to latest version.

Flags:
      --config string   config file (default is $HOME/.bizfly-backup.yaml)
      --debug           enable debug (default is false)
  -h, --help            help for bizfly-backup

Use "bizfly-backup [command] --help" for more information about a command.
```

# Agent

## Help

```shell script
$ ./bizfly-backup agent --help
Run agent.

Usage:
  bizfly-backup agent [flags]

Flags:
      --addr string   listening address of server. (default "http://localhost:29999")
  -h, --help          help for agent

Global Flags:
      --config string   config file (default is $HOME/.bizfly-backup.yaml)
      --debug           enable debug (default is false)
```
## Running

```shell script
$ ./bizfly-backup agent --debug=true --config=./conf/agent.yaml
2020-06-08T09:14:26.552+0700	INFO	cmd/root.go:96	Using config file: ./agent.yaml
2020-06-08T09:14:26.559+0700	DEBUG	cmd/agent.go:50	Listening address: http://localhost:29999
```

# Configuration Options

| Key | Default Value | Description                                                                                                                          |
|-----|---------------|--------------------------------------------------------------------------------------------------------------------------------------|
| machine_id | None          | machine_id is provided when create machine.                                                                                       |
| access_key | None          | access_key is provided when create machine.                                                                                          |
| secret_key | None          | secret_key is provided when create machine.                                                                                          |
| api_url | None          | api_url is provided when create machine.                                                                                               |
| limit_upload | unlimited     | limit_upload is used to limit upload bandwidth.                                                                                      |
| limit_download | unlimited     | limit_download is used to limit download bandwidth.                                                                                  |
| port | 29999          | port is used change the default port.                                                                                                |
| num_goroutine | calculated    | Quantity goroutine run at the same time. <br/>Default is caculated base on the number of logical CPUs usable by the current process. |

## Example

```shell script
access_key: GMWR7FNOUZ8QAT98VEHX
api_url: https://backup.bizflycloud.vn
machine_id: d1bfa61a-b0a6-4e64-b9f7-61d68037693a
secret_key: ef09a0fc5f013f0cac10f5c97ad04040bd72b4cc5a8e49b55ca1b644ea8779ff

limit_upload: 20000
limit_download: 30000

port: 29998

num_goroutine: 3
```
