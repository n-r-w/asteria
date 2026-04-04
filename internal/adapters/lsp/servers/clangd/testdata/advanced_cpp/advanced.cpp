#include "advanced.hpp"

#include <utility>

namespace demo {

Widget::Widget(std::string name, WidgetKind kind) : name_(std::move(name)), kind_(kind) {}

Widget::~Widget() = default;

std::string Widget::id() const {
    return name_;
}

int Widget::scale(int factor) const {
    return factor * default_scale;
}

Widget Widget::create(std::string name) {
    return Widget(std::move(name), WidgetKind::Basic);
}

Box make_box(int width, int height) {
    return Box{.width = width, .height = height};
}

int operate(int value) {
    return clamp_value(value, 0, 100);
}

int operate(int left, int right) {
    return left + right;
}

} // namespace demo
