#include "mixed_cpp.hpp"
#include "shared.h"

int MixedWidget::scale(int value) const {
    return value * 2;
}

int mixed_cpp_entry() {
    MixedWidget widget;
    SharedPayload payload{.value = shared_c_function(2)};

    return widget.scale(payload.value);
}
