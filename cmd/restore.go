// This file is part of bizfly-backup
//
// Copyright (C) 2020  BizFly Cloud
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// This program is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
// GNU General Public License for more details.
//
// You should have received a copy of the GNU General Public License
// along with this program.  If not, see <https://www.gnu.org/licenses/>

package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"strings"

	"github.com/spf13/cobra"
)

const postContentType = "application/octet-stream"

var restoreDir string

// restoreCmd represents the restore command
var restoreCmd = &cobra.Command{
	Use:   "restore",
	Short: "Restore a backup.",
	Run: func(cmd *cobra.Command, args []string) {
		// make url
		urlRequest := strings.Join([]string{addr, "recovery-points", recoveryPointID, "restore"}, "/")

		// create client
		httpc := http.Client{
			Transport: &http.Transport{
				DialContext: func(_ context.Context, _, _ string) (net.Conn, error) {
					return net.Dial(tcpProtocol, strings.TrimPrefix(addr, httpPrefix))
				},
			},
		}

		// init body
		if restoreDir == "" {
			restoreDir = strings.Join([]string{"bizfly-restore", recoveryPointID}, "/")
		}
		var body struct {
			Path string `json:"path"`
		}
		body.Path = restoreDir
		buf, _ := json.Marshal(body)
		// make request
		req, err := http.NewRequest(http.MethodPost, urlRequest, bytes.NewBuffer(buf))
		if err != nil {
			logger.Error(err.Error())
			os.Exit(1)
		}

		// call request
		resp, err := httpc.Do(req)
		if err != nil {
			logger.Error(err.Error())
			os.Exit(1)
		}

		defer resp.Body.Close()

		_, _ = io.Copy(os.Stderr, resp.Body)
	},
}

func init() {
	restoreCmd.PersistentFlags().StringVar(&restoreDir, "dest-directory", "", "The destination directory to restore")
	restoreCmd.PersistentFlags().StringVar(&recoveryPointID, "recovery-point-id", "", "The ID of recovery point")
	_ = restoreCmd.MarkPersistentFlagRequired("recovery-point-id")
	rootCmd.AddCommand(restoreCmd)
}
