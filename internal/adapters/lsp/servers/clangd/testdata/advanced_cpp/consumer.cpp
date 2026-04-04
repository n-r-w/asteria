#include "advanced.hpp"

int use_widget() {
    demo::Widget widget = demo::Widget::create("fixture");
    demo::Payload payload{.integer = 3};
    demo::Box box = demo::make_box(widget.scale(payload.integer), demo::operate(2, 4));

    return demo::operate(box.width) + static_cast<int>(demo::WidgetKind::Fancy);
}
