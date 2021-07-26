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

import "testing"

func Test_restoreSessionKey(t *testing.T) {
	type args struct {
		key             string
		machineID       string
		createdAt       string
		recoveryPointID string
	}
	tests := []struct {
		name string
		args args
		want string
	}{
		{
			name: "test restore session key",
			args: args{
				key:             "key",
				machineID:       "1",
				createdAt:       "2006-01-02 15:04:05.000000",
				recoveryPointID: "1",
			},
			want: "d06f1dd9decc13440b8ca5d659a9d0d43309be0c9e21fc1799f98e16511cb30a",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := restoreSessionKey(tt.args.key, tt.args.machineID, tt.args.createdAt, tt.args.recoveryPointID); got != tt.want {
				t.Errorf("restoreSessionKey() = %v, want %v", got, tt.want)
			}
		})
	}
}
