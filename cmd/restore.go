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
	"io/ioutil"
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
		httpc := http.Client{
			Transport: &http.Transport{
				DialContext: func(_ context.Context, _, _ string) (net.Conn, error) {
					return net.Dial("unix", strings.TrimPrefix(addr, "unix://"))
				},
			},
		}
		if restoreDir == "" {
			restoreDir = recoveryPointID
		}
		var body struct {
			Dest string `json:"destination"`
		}
		body.Dest = restoreDir
		buf, _ := json.Marshal(body)

		resp, err := httpc.Post("http://unix/recovery-points/"+recoveryPointID+"/restore", postContentType, bytes.NewBuffer(buf))
		if err != nil {
			logger.Error(err.Error())
			os.Exit(1)
		}
		defer resp.Body.Close()
		_, _ = io.Copy(ioutil.Discard, resp.Body)
	},
}

func init() {
	restoreCmd.PersistentFlags().StringVar(&restoreDir, "dest", "", "The destination to restore")
	restoreCmd.PersistentFlags().StringVar(&recoveryPointID, "recovery-point-id", "", "The ID of recovery point")
	restoreCmd.MarkPersistentFlagRequired("recovery-point-id")
	rootCmd.AddCommand(restoreCmd)
}
