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
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"

	"github.com/bizflycloud/bizfly-backup/pkg/backupapi"
	"github.com/bizflycloud/bizflyctl/formatter"
	"github.com/spf13/cobra"
)

var (
	actionID           string
	listActionsHeaders = []string{"ID", "Action", "Status", "RecoveryPointID", "PolicyID", "Progress", "Message"}
)

var actionCmd = &cobra.Command{
	Use:   "action",
	Short: "Perform action.",
	Run: func(cmd *cobra.Command, args []string) {
		if err := cmd.Help(); err != nil {
			logger.Error(err.Error())
		}
	},
}

var listActionCmd = &cobra.Command{
	Use:   "list",
	Short: "List all running action.",
	Run: func(cmd *cobra.Command, args []string) {
		// make url
		urlRequest := strings.Join([]string{addr, "actions"}, "/")

		// create client
		httpc := http.Client{
			Transport: &http.Transport{
				DialContext: func(_ context.Context, _, _ string) (net.Conn, error) {
					return net.Dial(tcpProtocol, strings.TrimPrefix(addr, httpPrefix))
				},
			},
		}

		// make request
		req, err := http.NewRequest(http.MethodGet, urlRequest, nil)
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

		var rla backupapi.ListActivity
		if err := json.NewDecoder(resp.Body).Decode(&rla); err != nil {
			_, err := fmt.Fprintln(os.Stderr, err.Error())
			if err != nil {
				return
			}
			os.Exit(1)
		}

		data := make([][]string, 0, len(rla.Activities))
		for _, ac := range rla.Activities {
			progress := ac.Progress

			if progress == "" && ac.Action != "RESTORE" {
				progress = ac.RecoveryPoint.Progress
			}

			if progress == "" {
				progress = "0%"
			}

			data = append(data, []string{ac.ID, ac.Action, ac.Status, ac.RecoveryPoint.ID, ac.PolicyID, progress, ac.Message})
		}

		formatter.Output(listActionsHeaders, data)
	},
}

var stopActionCmd = &cobra.Command{
	Use:   "stop",
	Short: "Stop specify action.",
	Run: func(cmd *cobra.Command, args []string) {
		if len(args) != 1 {
			fmt.Println("must specify one action_id")
		}

		// make url
		urlRequest := strings.Join([]string{addr, "actions", args[0]}, "/")

		// create client
		httpc := http.Client{
			Transport: &http.Transport{
				DialContext: func(_ context.Context, _, _ string) (net.Conn, error) {
					return net.Dial(tcpProtocol, strings.TrimPrefix(addr, httpPrefix))
				},
			},
		}

		// make request
		req, err := http.NewRequest(http.MethodDelete, urlRequest, nil)
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
	restoreCmd.PersistentFlags().StringVar(&actionID, "action_id", "", "The action_id of action want stop.")
	_ = restoreCmd.MarkPersistentFlagRequired("action_id")
	actionCmd.AddCommand(listActionCmd)
	actionCmd.AddCommand(stopActionCmd)
	rootCmd.AddCommand(actionCmd)
}
