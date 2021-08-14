package progress

import "testing"

func TestStat_String(t *testing.T) {
	type fields struct {
		Items    uint64
		Bytes    uint64
		Storage  uint64
		Errors   bool
		ItemName []string
	}
	tests := []struct {
		name   string
		fields fields
		want   string
	}{
		{
			name: "test stat string",
			fields: fields{
				Items:    1,
				Bytes:    1,
				Storage:  1,
				Errors:   true,
				ItemName: []string{"test"},
			},
			want: "Stat(1 items, item_name [test], bool error, 1B)",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := Stat{
				Items:    tt.fields.Items,
				Bytes:    tt.fields.Bytes,
				Storage:  tt.fields.Storage,
				Errors:   tt.fields.Errors,
				ItemName: tt.fields.ItemName,
			}
			if got := s.String(); got != tt.want {
				t.Errorf("Stat.String() = %v, want %v", got, tt.want)
			}
		})
	}
}
