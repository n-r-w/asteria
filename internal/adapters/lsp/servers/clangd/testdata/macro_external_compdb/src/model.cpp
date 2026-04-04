#include "../include/model.h"

MacroModel::MacroModel() = default;
MacroModel::~MacroModel() = default;

void MacroModel::setFastMode() {
    value_ = 1;
}

int MacroModel::value() const {
    return value_;
}
