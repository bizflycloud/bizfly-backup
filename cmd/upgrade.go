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
	"net"
	"net/http"
	"os"
	"strings"

	"github.com/spf13/cobra"
)

// upgradeCmd represents the upgrade command
var upgradeCmd = &cobra.Command{
	Use:   "upgrade",
	Short: "Upgrade bizfly-backup to latest version.",
	Run: func(cmd *cobra.Command, args []string) {
		// make url
		urlRequest := strings.Join([]string{addr, "upgrade"}, "/")

		// create client
		httpc := http.Client{
			Transport: &http.Transport{
				DialContext: func(_ context.Context, _, _ string) (net.Conn, error) {
					return net.Dial(tcpProtocol, strings.TrimPrefix(addr, httpPrefix))
				},
			},
		}

		// make request
		req, err := http.NewRequest(http.MethodPost, urlRequest, nil)
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
	},
}

func init() {
	rootCmd.AddCommand(upgradeCmd)
}
