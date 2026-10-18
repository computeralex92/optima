module github.com/computeralex92/optima

go 1.27.2

require github.com/bartolsthoorn/gohighs v1.15.1

// Temporary: upstream gohighs still bundles HiGHS 1.13.1.
// Drop when https://github.com/bartolsthoorn/gohighs/pull/5 ships.
replace github.com/bartolsthoorn/gohighs => github.com/computeralex92/gohighs v1.15.1
