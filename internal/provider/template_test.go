package provider

import (
	"testing"
)

var dashboard = Template{
	Workload: Workload{Resources: Resources{CPUCore: 1, RAMGB: 4, DiskGB: 10}},
	Services: []Service{
		{Name: "postgres", Resources: Resources{CPUCore: 1, RAMGB: 4, DiskGB: 10}},
		{Name: "redis", Resources: Resources{CPUCore: 1, RAMGB: 1, DiskGB: 2}},
	},
}

var dockerPrices = Prices{
	CPUCore: 4048,
	RAMGB:   4445,
	DiskGB:  110,
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
		want   int64
	}{
		{
			name:   "empty template",
			tpl:    Template{},
			prices: dockerPrices,
			want:   0,
		},
		{
			name:   "zero prices",
			tpl:    dashboard,
			prices: Prices{},
			want:   0,
		},
		{
			name:   "workload only",
			tpl:    Template{Workload: Workload{Resources: Resources{CPUCore: 1, RAMGB: 4, DiskGB: 10}}},
			prices: dockerPrices,
			want:   22928,
		},
		{
			name:   "workload and services",
			tpl:    dashboard,
			prices: dockerPrices,
			want:   54569,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.tpl.HourlyCost(tt.prices); got != tt.want {
				t.Errorf("HourlyCost() = %d, want %d", got, tt.want)
			}
		})
	}
}
