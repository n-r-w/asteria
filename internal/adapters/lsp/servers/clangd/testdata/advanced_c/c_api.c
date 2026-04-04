#include "c_api.h"

static int clamp_to_zero(int value) {
    return value < 0 ? 0 : value;
}

int apply_mode(Mode mode, int value) {
    if (mode == MODE_OFF) {
        return 0;
    }

    return clamp_to_zero(value);
}

Stats make_stats(int count, double average) {
    Stats stats = {count, average};

    return stats;
}
