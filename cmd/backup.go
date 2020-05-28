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

	"github.com/spf13/cobra"
)

// backupCmd represents the backup command
var backupCmd = &cobra.Command{
	Use:   "backup",
	Short: "Perform backup tasks.",
	Run: func(cmd *cobra.Command, args []string) {
		if err := cmd.Help(); err != nil {
			logger.Error(err.Error())
		}
	},
}

// backupListCmd represents the backup list command
var backupListCmd = &cobra.Command{
	Use:   "list",
	Short: "List all current backups.",
	Run: func(cmd *cobra.Command, args []string) {
		fmt.Println("list backups called")
	},
}

// backupRunCmd represents the backup run command
var backupRunCmd = &cobra.Command{
	Use:   "run",
	Short: "Run a backup immediately.",
	Run: func(cmd *cobra.Command, args []string) {
		fmt.Println("run backup called")
	},
}

func init() {
	rootCmd.AddCommand(backupCmd)
	backupCmd.AddCommand(backupListCmd)
	backupCmd.AddCommand(backupRunCmd)
}
