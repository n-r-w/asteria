#include "c_api.h"

int use_c_api(void) {
    struct Handler handler = {0};
    Stats stats = make_stats(3, 1.5);

    handler.transform = apply_mode;

    return handler.transform(MODE_ON, stats.count);
}
