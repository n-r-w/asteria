#pragma once

typedef enum Mode {
    MODE_OFF,
    MODE_ON,
} Mode;

typedef struct Stats {
    int count;
    double average;
} Stats;

union Number {
    int integer;
    double floating;
};

typedef int (*transform_fn)(Mode mode, int value);

struct Handler {
    transform_fn transform;
};

int apply_mode(Mode mode, int value);
Stats make_stats(int count, double average);
