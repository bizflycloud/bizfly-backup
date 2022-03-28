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
	"fmt"
	"github.com/bizflycloud/bizfly-backup/pkg/agentversion"

	"github.com/spf13/cobra"
)

var versionCmd = &cobra.Command{
	Use:   "version",
	Short: "Print current version.",
	Run: func(cmd *cobra.Command, args []string) {
		if agentversion.CurrentVersion == "" {
			agentversion.CurrentVersion = "dev"
		}
		fmt.Println("Version: ", agentversion.CurrentVersion)
		fmt.Println("Git commit: ", agentversion.GitCommit)
		fmt.Println("Build: ", agentversion.BuildTime)
	},
}

func init() {
	rootCmd.AddCommand(versionCmd)
}
