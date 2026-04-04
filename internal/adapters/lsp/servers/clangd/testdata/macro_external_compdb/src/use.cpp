#include "../include/model.h"

int use_model() {
    MacroModel model;
    model.setFastMode();

    return model.value();
}
