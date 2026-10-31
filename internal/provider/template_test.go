package provider

import (
	"testing"

	"github.com/shopspring/decimal"
)

var dashboard = Template{
	Workload: Workload{Resources: Resources{CPUCore: 1, RAMGB: 4, DiskGB: 10}},
	Services: []Service{
		{Name: "postgres", Resources: Resources{CPUCore: 1, RAMGB: 4, DiskGB: 10}},
		{Name: "redis", Resources: Resources{CPUCore: 1, RAMGB: 1, DiskGB: 2}},
	},
}

var dockerPrices = Prices{
	CPUCore: decimal.RequireFromString("0.004048"),
	RAMGB:   decimal.RequireFromString("0.004445"),
	DiskGB:  decimal.RequireFromString("0.00011"),
}

func TestTotalResources(t *testing.T) {
	tests := []struct {
		name string
		tpl  Template
		want Resources
	}{
		{
			name: "empty",
			tpl:  Template{},
			want: Resources{},
		},
		{
			name: "workload only",
			tpl:  Template{Workload: Workload{Resources: Resources{CPUCore: 2, RAMGB: 8, DiskGB: 20}}},
			want: Resources{CPUCore: 2, RAMGB: 8, DiskGB: 20},
		},
		{
			name: "workload and services",
			tpl:  dashboard,
			want: Resources{CPUCore: 3, RAMGB: 9, DiskGB: 22},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.tpl.TotalResources(); got != tt.want {
				t.Errorf("TotalResources() = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestTotalResourcesDoesNotMutateWorkload(t *testing.T) {
	tpl := dashboard
	before := tpl.Workload.Resources
	tpl.TotalResources()
	if tpl.Workload.Resources != before {
		t.Errorf("workload resources changed to %+v, want %+v", tpl.Workload.Resources, before)
	}
}

func TestHourlyCost(t *testing.T) {
	tests := []struct {
		name   string
		tpl    Template
		prices Prices
		want   string
	}{
		{
			name:   "empty template",
			tpl:    Template{},
			prices: dockerPrices,
			want:   "0",
		},
		{
			name:   "zero prices",
			tpl:    dashboard,
			prices: Prices{},
			want:   "0",
		},
		{
			name:   "workload only",
			tpl:    Template{Workload: Workload{Resources: Resources{CPUCore: 1, RAMGB: 4, DiskGB: 10}}},
			prices: dockerPrices,
			want:   "0.022928",
		},
		{
			name:   "workload and services",
			tpl:    dashboard,
			prices: dockerPrices,
			want:   "0.054569",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			want := decimal.RequireFromString(tt.want)
			if got := tt.tpl.HourlyCost(tt.prices); !got.Equal(want) {
				t.Errorf("HourlyCost() = %s, want %s", got, want)
			}
		})
	}
}
